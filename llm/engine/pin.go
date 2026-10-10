package engine

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// pinFS is the committed pin: opencoti's pin format 2 (their
// docs/protocols/PIN_FORMAT.md). pin/index.txt is OUR index -- which
// components this tree takes, each by the sha256 of its pin file -- and the
// component pin files beside it are byte-identical copies of the published
// ones.
//
// It is embedded rather than read from disk because its consumers are a
// running server and a test, neither of which can assume the source tree is
// present. cmake/opencoti-fetch.cmake reads the same directory with the same
// rules at build time; TestCMakeStagesWhatGoReads holds the two to one answer.
//
//go:embed pin/*.txt
var pinFS embed.FS

// pinDir is where the pin lives in pinFS.
const pinDir = "pin"

// Asset is one published file of the pinned engine.
type Asset struct {
	Kind   string // "bin" (the engine), "dso" (a GPU library), "dso-cuda12" or "sidecar"
	Arch   string // x86_64 | aarch64 | win-x86_64 | macos-aarch64, a dso with its backend: x86_64-vulkan
	Path   string // path within the Hugging Face repo
	SHA256 string
	Bytes  int64
	// Role is the pin's own kind for a sidecar (SidecarCodec, "licence",
	// "build-info", or a kind a later component adds). Empty on the others.
	Role string
	// For names the kind a licence text belongs to (`for espeak`).
	For string
	// Component and Version say which component pin the row is from; Repo and
	// Rev where its bytes are fetched. Components move one at a time, so two
	// assets of one pin can come from two commits.
	Component string
	Version   string
	Repo      string
	Rev       string
}

// StagedName is the file name the asset has beside the engine: the name it was
// published with, never another. That name is the one the engine looks for,
// and it is unique across the components of an index.
func (a Asset) StagedName() string { return path.Base(a.Path) }

// Pin is the parsed pin: where the files live, which bytes are the right ones,
// and what that build can be asked to do.
type Pin struct {
	// Repo and Rev are the engine component's. Every Asset carries its own.
	Repo string
	Rev  string
	// Tag is the engine's file name, which names its cut and build
	// (opencoti-0.10.5-c7-2610040837001 on the dev channel,
	// opencoti-llamafile-0.10.5-c8-bare.llamafile on a release). Version is
	// the build id alone.
	Tag     string
	Version string
	// Channel is release or dev, from the index. It is declared rather than
	// guessed from the repo name because the two channels may share a repo.
	Channel string
	// Features are the capabilities the engine's pin states for every
	// platform. See HasFeatureOn for the ones a pin limits to some.
	Features         []string
	platformFeatures map[string][]string
	// Accels are the backends the pinned files accelerate, per arch: a GPU
	// library's row is the claim that the backend exists there. It is what
	// stops routing handing a GPU load to an engine that would quietly serve
	// it on the CPU.
	Accels []Accel
	// CUDASASS lists the compute capabilities the CUDA library carries SASS
	// for, as major*10+minor, from the cuda component's sass line. Empty
	// means the pin does not narrow them. See CoversCUDA.
	CUDASASS []int
	// CUDA12SASS is the same list for the legacy CUDA 12 library: the older
	// cards the CUDA 13 one has no code for. Both libraries sit beside one
	// engine, and one process loads one of them (cudaPayload, LegacyCUDAEnv).
	CUDA12SASS []int
	// SBSASASS is the list of the sbsa component: the CUDA library of Linux
	// aarch64 (DGX Spark, Jetson Thor). It is the one CoversCUDA reads on that
	// platform. opencoti has never run that library and neither have we: no
	// such machine is in either test fleet (owner, 2026-10-10: built blind).
	SBSASASS []int
	Assets   []Asset
}

// hostPackageArch is the pin platform of the running binary, "" where no
// engine is packaged. A variable so a test can stand on another platform.
var hostPackageArch = func() string {
	arch, _ := PackageArch(runtime.GOOS, runtime.GOARCH)
	return arch
}

// HostCUDASASS is the sass list of the CUDA library this host loads: the sbsa
// component's on Linux aarch64, the cuda component's everywhere else.
func (p Pin) HostCUDASASS() []int {
	if hostPackageArch() == "aarch64" {
		return p.SBSASASS
	}
	return p.CUDASASS
}

// Accel is one (arch, backend) pair the pinned artifact accelerates.
type Accel struct {
	Arch    string
	Backend Backend
}

// Channel values.
const (
	ChannelRelease = "release"
	ChannelDev     = "dev"
)

// Accelerates reports whether the pinned artifact carries a payload that runs
// this backend on this arch. CPU is always true: the host binary is the engine.
func (p Pin) Accelerates(arch string, b Backend) bool {
	if b == BackendCPU {
		return true
	}
	return slices.Contains(p.Accels, Accel{Arch: arch, Backend: b})
}

// CoversCUDA reports whether the pinned CUDA payload carries code for a device
// of this compute capability. SASS for major.minor runs on the same major at
// that minor or later, and on nothing else: sm_86 serves 8.6-8.9, sm_120f
// serves 12.x, and neither serves 8.0 or 9.0. A pin without a sass line does
// not narrow anything.
//
// The failure this prevents is silent: a device the DSO has no code for makes
// the engine run the load on the CPU, which reads as slowness, not as an error.
func (p Pin) CoversCUDA(major, minor int) bool {
	sass := p.HostCUDASASS()
	if len(sass) == 0 {
		return true
	}
	for _, cc := range sass {
		// The sbsa library's 121 is sm_121a, the architecture-specific image:
		// it runs on 12.1 exactly and the file carries no PTX to compile from
		// (opencoti mail #952, cuobjdump of the published bytes).
		if hostPackageArch() == "aarch64" && cc == 121 {
			if major == 12 && minor == 1 {
				return true
			}
			continue
		}
		if major == cc/10 && minor >= cc%10 {
			return true
		}
	}
	return false
}

// CoversCUDA12 reports whether the pin's CUDA 12 payload carries code for a
// device of this compute capability, with CoversCUDA's matching rule. Unlike
// CoversCUDA, a pin that states no sass for it covers nothing: the CUDA 12
// payload is optional and only ever serves what it names.
func (p Pin) CoversCUDA12(major, minor int) bool {
	// The payload of this host: Linux x86_64 is the only platform that has one.
	if _, ok := p.CUDA12DSO(hostPackageArch()); !ok {
		return false
	}
	for _, cc := range p.CUDA12SASS {
		if major == cc/10 && minor >= cc%10 {
			return true
		}
	}
	return false
}

// CUDA12DSO returns the CUDA 12 payload for an arch label, if the pin has one.
func (p Pin) CUDA12DSO(arch string) (Asset, bool) {
	for _, a := range p.Assets {
		if a.Kind == kindCUDA12DSO && a.Arch == arch {
			return a, true
		}
	}
	return Asset{}, false
}

// Asset kinds. A pin's file kinds map onto them in assetOf.
const (
	kindBin       = "bin"
	kindDSO       = "dso"
	kindCUDA12DSO = "dso-cuda12"
	// kindSidecar is every other file staged beside the engine: the media
	// libraries it loads from its own directory under their published names,
	// the macOS loader and Metal library, the licence texts.
	kindSidecar = "sidecar"
)

// What a sidecar is. The kinds are opencoti's and the list is open: a row of
// a kind this build has never heard of is still a file the engine wants
// beside it, so it is staged, never checked against these names.
const (
	// SidecarCodec encodes and decodes: mp3, opus and aac speech, mp4 video.
	SidecarCodec = "codec"
	// SidecarAPE is the macOS loader the engine is started through
	// (MacLoader); SidecarMetal is its Metal backend library.
	SidecarAPE   = "ape"
	SidecarMetal = "metal"
	// SidecarAudioCpp is audio.cpp: Kokoro, Supertonic and KittenTTS.
	SidecarAudioCpp = "audiocpp"
	// SidecarLicence is a licence text; Asset.For names what it covers.
	SidecarLicence = "licence"
)

// Sidecars returns the files the pin stages beside the engine for an arch
// label, other than the engine and its GPU libraries, in pin order.
func (p Pin) Sidecars(arch string) []Asset {
	var out []Asset
	for _, a := range p.Assets {
		if a.Kind == kindSidecar && a.Arch == arch {
			out = append(out, a)
		}
	}
	return out
}

// Files returns everything the pin stages beside the engine for an arch
// label, the engine included. It is what cmake/opencoti-fetch.cmake stages.
func (p Pin) Files(arch string) []Asset {
	var out []Asset
	for _, a := range p.Assets {
		if dsoArch, _ := splitDSOLabel(a.Arch); a.Arch == arch || (a.Kind == kindDSO && dsoArch == arch) {
			out = append(out, a)
		}
	}
	return out
}

// HasFeature reports whether the pinned engine declares a capability on every
// platform it runs on.
func (p Pin) HasFeature(name string) bool {
	return slices.Contains(p.Features, name)
}

// HasFeatureOn reports whether the pinned engine declares a capability for an
// arch label: one it has everywhere, or one its pin lists that platform for
// (`feature images_generate_v1 x86_64 win-x86_64`).
func (p Pin) HasFeatureOn(name, arch string) bool {
	return p.HasFeature(name) || slices.Contains(p.platformFeatures[name], arch)
}

// DefaultPin is the pin compiled into this binary.
var DefaultPin = sync.OnceValues(func() (Pin, error) { return LoadPin(pinFS, pinDir) })

// components are the component names of pin format 2, in the order an index
// lists them.
var components = []string{"engine", "cuda", "cuda12", "sbsa", "vulkan", "macos", "media"}

// platforms are the platform labels of a `file` row, `any` aside.
var platforms = []string{"x86_64", "aarch64", "win-x86_64", ArchMacOS}

// fixedArity is the number of fields each fixed KEY takes. Extra trailing
// fields are ignored: that is the format's room for later columns.
var fixedArity = map[string]int{
	"format": 1, "component": 1, "version": 1, "built-from": 1, "repo": 1, "rev": 1,
	"abi": 2, "abi-source": 1, "engine-min": 1, "gated-with": 1, "requires": 3, "file": 5,
	"channel": 1, "tag": 1,
}

// variableKeys take as many fields as they are given.
var variableKeys = []string{"feature", "absent", "sass"}

// statement is one line of a pin or index file.
type statement struct {
	key    string
	fields []string
	line   int
}

// statements reads the syntax both file kinds share: one statement per line,
// '#' starts a comment, an unknown KEY is an error, the first statement is
// `format 2`.
func statements(name, text string) ([]statement, error) {
	var out []statement
	for n, raw := range strings.Split(text, "\n") {
		if i := strings.IndexByte(raw, '#'); i >= 0 {
			raw = raw[:i]
		}
		f := strings.Fields(raw)
		if len(f) == 0 {
			continue
		}
		key, fields := f[0], f[1:]
		switch arity, fixed := fixedArity[key]; {
		case fixed && len(fields) < arity:
			return nil, fmt.Errorf("%s:%d: %s needs %d field(s), got %d", name, n+1, key, arity, len(fields))
		case !fixed && !slices.Contains(variableKeys, key) && !slices.Contains(components, key):
			return nil, fmt.Errorf("%s:%d: unknown KEY %q", name, n+1, key)
		}
		out = append(out, statement{key: key, fields: fields, line: n + 1})
	}
	if len(out) == 0 || out[0].key != "format" || out[0].fields[0] != "2" {
		return nil, fmt.Errorf("%s: the first statement must be `format 2`", name)
	}
	return out[1:], nil
}

// fileRow is a `file` row of a component pin.
type fileRow struct {
	platform, kind, path, sha256, forKind string
	bytes                                 int64
}

// component is one parsed component pin.
type component struct {
	name, version, repo, rev, engineMin string
	abi                                 map[string]string
	sass                                []int
	features                            [][]string
	files                               []fileRow
}

// indexEntry is one component line of an index.
type indexEntry struct {
	name, file, sha256, version string
}

// parseIndex reads an index: the channel and, per component, the pin file it
// names or that it is absent.
func parseIndex(name, text string) (channel string, entries []indexEntry, err error) {
	sts, err := statements(name, text)
	if err != nil {
		return "", nil, err
	}
	var tag string
	for _, st := range sts {
		switch {
		case st.key == "channel":
			channel = st.fields[0]
		case st.key == "tag":
			tag = st.fields[0]
		case slices.Contains(components, st.key):
			if slices.ContainsFunc(entries, func(e indexEntry) bool { return e.name == st.key }) {
				return "", nil, fmt.Errorf("%s:%d: a second %s line", name, st.line, st.key)
			}
			f := st.fields
			if len(f) == 1 && f[0] == "absent" {
				continue
			}
			if len(f) < 7 || f[1] != "rev" || f[3] != "sha256" || f[5] != "version" {
				return "", nil, fmt.Errorf("%s:%d: %s is neither `absent` nor `<pin path> rev <commit> sha256 <digest> version <id>`", name, st.line, st.key)
			}
			if !isCommitRev(f[2]) || !isSHA256(f[4]) || !isBuildID(f[6]) {
				return "", nil, fmt.Errorf("%s:%d: bad rev, sha256 or version on the %s line", name, st.line, st.key)
			}
			entries = append(entries, indexEntry{name: st.key, file: path.Base(f[0]), sha256: f[4], version: f[6]})
		default:
			return "", nil, fmt.Errorf("%s:%d: %s is not an index KEY", name, st.line, st.key)
		}
	}
	// A channel is required rather than defaulted, so a dev pin can never be
	// mistaken for a release one by omission.
	if channel != ChannelRelease && channel != ChannelDev {
		return "", nil, fmt.Errorf("%s: channel %q is not %s or %s", name, channel, ChannelRelease, ChannelDev)
	}
	if tag == "" {
		return "", nil, fmt.Errorf("%s: no tag", name)
	}
	if !slices.ContainsFunc(entries, func(e indexEntry) bool { return e.name == "engine" }) {
		return "", nil, fmt.Errorf("%s: an index needs the engine", name)
	}
	return channel, entries, nil
}

// parseComponent reads one component pin.
func parseComponent(name, text string) (component, error) {
	sts, err := statements(name, text)
	if err != nil {
		return component{}, err
	}
	c := component{abi: map[string]string{}}
	var abiSource, builtFrom string
	for _, st := range sts {
		f := st.fields
		switch st.key {
		case "component":
			c.name = f[0]
		case "version":
			c.version = f[0]
		case "repo":
			c.repo = f[0]
		case "rev":
			c.rev = f[0]
		case "built-from":
			builtFrom = f[0]
		case "engine-min":
			c.engineMin = f[0]
		case "abi-source":
			abiSource = f[0]
		case "abi":
			if !isSHA256(f[1]) {
				return component{}, fmt.Errorf("%s:%d: abi %s digest is not 64 lowercase hex characters", name, st.line, f[0])
			}
			c.abi[f[0]] = f[1]
		case "sass":
			for _, v := range f {
				cc, err := strconv.Atoi(v)
				if err != nil || cc < 10 {
					return component{}, fmt.Errorf("%s:%d: sass %q is not a compute capability written as major*10+minor (86, 120)", name, st.line, v)
				}
				c.sass = append(c.sass, cc)
			}
		case "feature":
			if len(f) == 0 {
				return component{}, fmt.Errorf("%s:%d: feature needs a name", name, st.line)
			}
			c.features = append(c.features, f)
		case "file":
			row := fileRow{platform: f[0], kind: f[1], path: f[2], sha256: f[3]}
			if row.platform != "any" && !slices.Contains(platforms, row.platform) {
				return component{}, fmt.Errorf("%s:%d: unknown platform %q", name, st.line, row.platform)
			}
			// The file is staged under this name and found by it, so a row
			// whose path or digest is off ships an engine without the file.
			if strings.HasPrefix(row.path, "/") || strings.Contains(row.path, "..") {
				return component{}, fmt.Errorf("%s:%d: path %q must be a plain repo-relative path", name, st.line, row.path)
			}
			if row.bytes, err = strconv.ParseInt(f[4], 10, 64); err != nil || row.bytes < 0 || !isSHA256(row.sha256) {
				return component{}, fmt.Errorf("%s:%d: bad sha256 or size for %s", name, st.line, row.path)
			}
			if len(f) >= 7 && f[5] == "for" {
				row.forKind = f[6]
			}
			for _, have := range c.files {
				if have.platform == row.platform && have.kind == row.kind && path.Base(have.path) == path.Base(row.path) {
					return component{}, fmt.Errorf("%s:%d: a second %s %s row for %s", name, st.line, row.platform, row.kind, path.Base(row.path))
				}
			}
			c.files = append(c.files, row)
		case "gated-with", "requires", "absent":
			// What it was tested with, what the host must provide, and what
			// was not built: stated for the reader, not gated on here.
		default:
			return component{}, fmt.Errorf("%s:%d: %s is not a component KEY", name, st.line, st.key)
		}
	}
	switch {
	case !slices.Contains(components, c.name):
		return component{}, fmt.Errorf("%s: unknown component %q", name, c.name)
	case !isBuildID(c.version):
		return component{}, fmt.Errorf("%s: version %q is not a 13-digit build id", name, c.version)
	case c.repo == "":
		return component{}, fmt.Errorf("%s: no repo", name)
	// The revision must be an immutable commit, never a branch: opencoti has
	// re-cut a release in place, which turned every pin that said `rev main`
	// into a build that fetches bytes its own sha256 rows reject.
	case !isCommitRev(c.rev):
		return component{}, fmt.Errorf("%s: rev %q is not a 40-character commit sha; a branch or tag can be moved under the pinned sha256 rows", name, c.rev)
	case abiSource != "computed" && abiSource != "exported":
		return component{}, fmt.Errorf("%s: abi-source %q is not computed or exported", name, abiSource)
	case len(c.abi) == 0:
		return component{}, fmt.Errorf("%s: no abi line", name)
	case len(c.files) == 0:
		return component{}, fmt.Errorf("%s: no file row", name)
	case c.name != "engine" && (!isBuildID(builtFrom) || !isBuildID(c.engineMin)):
		return component{}, fmt.Errorf("%s: built-from or engine-min is missing or not a build id", name)
	}
	return c, nil
}

// rowsFor returns the rows of a component a platform stages. A component with
// no row for the platform itself is not taken there at all (its BUILD_INFO
// alone is not a payload); the engine is one file for every platform, so it
// always is. A bin is the platform's own row when there is one, else `any`.
func (c component) rowsFor(platform string) []fileRow {
	own, ownBin := false, false
	for _, r := range c.files {
		if r.platform == platform {
			own = true
			ownBin = ownBin || r.kind == kindBin
		}
	}
	if !own && c.name != "engine" {
		return nil
	}
	var out []fileRow
	for _, r := range c.files {
		switch {
		case r.platform != platform && r.platform != "any":
		case r.kind == kindBin && r.platform == "any" && ownBin:
		default:
			out = append(out, r)
		}
	}
	return out
}

// assetOf turns a component's file row into the asset routing knows: the
// engine, a GPU library labelled <arch>[-<backend>] with the bare label CUDA,
// the CUDA 12 library, or a sidecar carrying the pin's own kind.
func (c component) assetOf(platform string, r fileRow) Asset {
	a := Asset{
		Arch: platform, Path: r.path, SHA256: r.sha256, Bytes: r.bytes,
		Component: c.name, Version: c.version, Repo: c.repo, Rev: c.rev,
	}
	switch r.kind {
	case kindBin:
		a.Kind = kindBin
	case "cuda":
		a.Kind = kindDSO
	case "vulkan":
		a.Kind, a.Arch = kindDSO, platform+"-vulkan"
	case "cuda12":
		a.Kind = kindCUDA12DSO
	default:
		a.Kind, a.Role, a.For = kindSidecar, r.kind, r.forKind
	}
	return a
}

// LoadPin reads a pin directory: the index and the component pins it names.
//
// It refuses what the format says a consumer must refuse: a vendored pin file
// whose bytes are not the ones the index names, a component whose abi the
// engine does not provide, and one built for a newer engine than the index
// takes. A library and an engine that disagree on an interface do not fail to
// load; they crash or run wrong.
func LoadPin(fsys fs.FS, dir string) (Pin, error) {
	read := func(name string) (string, error) {
		b, err := fs.ReadFile(fsys, path.Join(dir, name))
		return string(b), err
	}
	text, err := read("index.txt")
	if err != nil {
		return Pin{}, fmt.Errorf("pin: %w", err)
	}
	channel, entries, err := parseIndex("index.txt", text)
	if err != nil {
		return Pin{}, err
	}

	taken := make([]component, 0, len(entries))
	for _, e := range entries {
		text, err := read(e.file)
		if err != nil {
			return Pin{}, fmt.Errorf("pin: the index names %s: %w", e.file, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(text))); got != e.sha256 {
			return Pin{}, fmt.Errorf("pin: %s is not the pin the index names: sha256 %s, the index says %s", e.file, got, e.sha256)
		}
		c, err := parseComponent(e.file, text)
		if err != nil {
			return Pin{}, err
		}
		if c.name != e.name || c.version != e.version {
			return Pin{}, fmt.Errorf("pin: the index names %s %s, %s is %s %s", e.name, e.version, e.file, c.name, c.version)
		}
		taken = append(taken, c)
	}
	eng := taken[slices.IndexFunc(taken, func(c component) bool { return c.name == "engine" })]
	for _, c := range taken {
		if c.name == "engine" {
			continue
		}
		for name, digest := range c.abi {
			if eng.abi[name] != digest {
				return Pin{}, fmt.Errorf("pin: %s %s needs abi %s %s, and engine %s provides %q", c.name, c.version, name, digest, eng.version, eng.abi[name])
			}
		}
		// Build ids are decimal strings of one length, so they order as text.
		if eng.version < c.engineMin {
			return Pin{}, fmt.Errorf("pin: %s %s needs engine %s or newer, the index names %s", c.name, c.version, c.engineMin, eng.version)
		}
	}

	p := Pin{Repo: eng.repo, Rev: eng.rev, Version: eng.version, Channel: channel, platformFeatures: map[string][]string{}}
	for _, f := range eng.features {
		if len(f) == 1 {
			p.Features = append(p.Features, f[0])
		} else {
			p.platformFeatures[f[0]] = append(p.platformFeatures[f[0]], f[1:]...)
		}
	}
	for _, c := range taken {
		switch c.name {
		case "cuda":
			p.CUDASASS = c.sass
		case "cuda12":
			p.CUDA12SASS = c.sass
		case "sbsa":
			p.SBSASASS = c.sass
		}
		for _, platform := range platforms {
			for _, r := range c.rowsFor(platform) {
				a := c.assetOf(platform, r)
				if a.Kind == kindBin && p.Tag == "" {
					p.Tag = strings.TrimSuffix(path.Base(a.Path), ".exe")
				}
				p.Assets = append(p.Assets, a)
			}
		}
	}
	if p.Tag == "" {
		return Pin{}, fmt.Errorf("pin: the engine component has no bin row")
	}
	// One staging directory: a name two components of a platform share would
	// have one file overwrite the other beside the engine.
	for _, platform := range platforms {
		seen := map[string]string{}
		for _, a := range p.Files(platform) {
			if other, dup := seen[a.StagedName()]; dup {
				return Pin{}, fmt.Errorf("pin: %s and %s both stage %s for %s", other, a.Component, a.StagedName(), platform)
			}
			seen[a.StagedName()] = a.Component
		}
	}
	p.deriveAccels()

	return p, nil
}

// deriveAccels states the (arch, backend) pairs the pin's own rows prove: a
// GPU library for a platform the pin ships an engine for.
//
// The libraries shipped are the ground truth about what the engine can
// accelerate. Without this a pin accelerates nothing, and every GPU load
// routes to llama.cpp while the pinned engine sits there holding a working
// payload.
func (p *Pin) deriveAccels() {
	for _, a := range p.Assets {
		var accel Accel
		switch {
		case a.Kind == kindDSO:
			accel.Arch, accel.Backend = splitDSOLabel(a.Arch)
		case a.Kind == kindSidecar && a.Role == SidecarMetal:
			accel = Accel{Arch: a.Arch, Backend: BackendMetal}
		default:
			continue
		}
		// A payload for a platform this pin ships no engine for accelerates
		// nothing here.
		if _, ok := p.Asset(accel.Arch); !ok {
			continue
		}
		if !slices.Contains(p.Accels, accel) {
			p.Accels = append(p.Accels, accel)
		}
	}
}

// splitDSOLabel reads a GPU library's label as <arch>[-<backend>]; the bare
// form is CUDA.
func splitDSOLabel(label string) (arch string, backend Backend) {
	for suffix, b := range map[string]Backend{
		"-vulkan": BackendVulkan,
		"-rocm":   BackendROCm,
	} {
		if rest, ok := strings.CutSuffix(label, suffix); ok {
			return rest, b
		}
	}
	return label, BackendCUDA
}

func isHex(s string, n int) bool {
	return len(s) == n && strings.TrimLeft(s, "0123456789abcdef") == ""
}

func isSHA256(s string) bool    { return isHex(s, 64) }
func isCommitRev(s string) bool { return isHex(s, 40) }

// isBuildID reports whether s is a build id: 13 decimal digits, a timestamp
// and a counter, which is what lets two of them be compared as strings.
func isBuildID(s string) bool {
	return len(s) == 13 && strings.TrimLeft(s, "0123456789") == ""
}

// Asset returns the engine for an arch label. GPU libraries are addressed
// with DSO: an arch has both, and the engine is always the bin.
func (p Pin) Asset(arch string) (Asset, bool) {
	for _, a := range p.Assets {
		if a.Kind == kindBin && a.Arch == arch {
			return a, true
		}
	}
	return Asset{}, false
}

// DSO returns the GPU library with a label (x86_64 is CUDA's,
// x86_64-vulkan Vulkan's), if the pin carries one.
func (p Pin) DSO(label string) (Asset, bool) {
	for _, a := range p.Assets {
		if a.Kind == kindDSO && a.Arch == label {
			return a, true
		}
	}
	return Asset{}, false
}

// URL is where a file is fetched from at build time. Hugging Face is the only
// source: the artifacts are larger than GitHub's release-asset cap.
func (p Pin) URL(a Asset) string {
	repo, rev := a.Repo, a.Rev
	if repo == "" {
		repo, rev = p.Repo, p.Rev
	}
	return "https://huggingface.co/" + repo + "/resolve/" + rev + "/" + a.Path
}

// ArchMacOS is the arch label of the macOS arm64 package. Its engine is the
// same APE file as everywhere else; what makes it a package of its own is the
// loader and the Metal library beside it.
const ArchMacOS = "macos-aarch64"

// PackageArch maps a build host onto the arch label packaged for it: the
// platform column of a pin's `file` rows.
//
// macOS is Apple silicon only: opencoti publishes its loader, Metal library and
// media sidecars for arm64 and nothing for Intel, so darwin/amd64 has no label
// and stays on llama.cpp.
func PackageArch(goos, goarch string) (string, error) {
	switch {
	case goos == "linux" && goarch == "amd64":
		return "x86_64", nil
	case goos == "linux" && goarch == "arm64":
		return "aarch64", nil
	case goos == "windows" && goarch == "amd64":
		return "win-x86_64", nil
	case goos == "darwin" && goarch == "arm64":
		return ArchMacOS, nil
	}
	return "", fmt.Errorf("no opencoti-llamafile artifact is packaged for %s/%s", goos, goarch)
}
