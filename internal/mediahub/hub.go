// Package mediahub finds and names media components on Hugging Face
// (plans/media-integration.md): a reference to one file of a repo, its
// sha256 and size, a search of the hub by task, a repo's file list, and the
// curated catalog of media templates.
//
// The server does not download through this package. Hugging Face's
// ollama-compatible registry serves every large file of a repo by its sha256
// at hf.co/v2/<repo>/blobs/<digest> -- GGUF, safetensors and whisper.cpp .bin
// alike -- so the server fetches a component with the downloader every hf.co
// pull already uses, and this package only has to name the digest. The one
// download here is the client's local mirror (mirror.go).
package mediahub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Endpoint is the hub's web and API host. HF_ENDPOINT, huggingface_hub's own
// variable, points it at a mirror.
func Endpoint() string {
	if e := strings.TrimRight(os.Getenv("HF_ENDPOINT"), "/"); e != "" {
		return e
	}
	return "https://huggingface.co"
}

// Ref names one file of a Hugging Face model repo at a revision.
type Ref struct {
	Repo string // owner/name
	Path string // the file inside the repo
	Rev  string // branch, tag or commit; "main" when unstated
}

// String is the canonical form, hf.co/<owner>/<name>/<path>[@<rev>].
func (r Ref) String() string {
	s := "hf.co/" + r.Repo + "/" + r.Path
	if r.Rev != "" && r.Rev != "main" {
		s += "@" + r.Rev
	}
	return s
}

// Registry is the name the server's downloader pulls the file's blob under.
func (r Ref) Registry() string { return "hf.co/" + r.Repo }

var errNotRef = errors.New("not a Hugging Face file reference")

// IsRef reports whether s names a Hugging Face file rather than a local path
// or a digest.
func IsRef(s string) bool {
	_, err := ParseRef(s)
	return err == nil
}

// ParseRef reads hf.co/<owner>/<name>/<path>[@rev], the same with
// huggingface.co, or a browser URL .../<owner>/<name>/(resolve|blob)/<rev>/<path>.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	var rest string
	switch {
	case strings.HasPrefix(s, "hf.co/"):
		rest = strings.TrimPrefix(s, "hf.co/")
	case strings.HasPrefix(s, "huggingface.co/"):
		rest = strings.TrimPrefix(s, "huggingface.co/")
	default:
		return Ref{}, errNotRef
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Ref{}, fmt.Errorf("%q: want hf.co/<owner>/<repo>/<file>", s)
	}
	r := Ref{Repo: parts[0] + "/" + parts[1], Path: parts[2], Rev: "main"}
	// A browser URL: <owner>/<name>/resolve/<rev>/<path>.
	if seg := strings.SplitN(r.Path, "/", 3); len(seg) == 3 && (seg[0] == "resolve" || seg[0] == "blob") {
		r.Rev, r.Path = seg[1], seg[2]
	} else if p, rev, ok := strings.Cut(r.Path, "@"); ok {
		r.Path, r.Rev = p, rev
	}
	if r.Path == "" || r.Rev == "" || strings.Contains(r.Path, "..") {
		return Ref{}, fmt.Errorf("%q: want hf.co/<owner>/<repo>/<file>[@rev]", s)
	}
	return r, nil
}

// File is a resolved reference: what to download, and what to call it.
type File struct {
	Ref
	Digest string // sha256:<hex>
	Size   int64
	Commit string // the commit the revision named when it was resolved
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Resolve asks the hub for the file's sha256 and size, from the headers of
// its download URL (the redirect is not followed; nothing is downloaded).
func Resolve(ctx context.Context, client *http.Client, r Ref) (File, error) {
	u := Endpoint() + "/" + r.Repo + "/resolve/" + url.PathEscape(r.Rev) + "/" + escapePath(r.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return File{}, err
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return File{}, err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return File{}, fmt.Errorf("%s: no such file on Hugging Face", r)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return File{}, fmt.Errorf("%s: the repo is gated or private (%d)", r, resp.StatusCode)
	case resp.StatusCode >= 400:
		return File{}, fmt.Errorf("%s: Hugging Face answered %d", r, resp.StatusCode)
	}
	etag := strings.Trim(resp.Header.Get("X-Linked-Etag"), `"`)
	if strings.HasPrefix(etag, "W/") {
		etag = strings.Trim(strings.TrimPrefix(etag, "W/"), `"`)
	}
	if !sha256Hex.MatchString(etag) {
		// A small file kept in git, not LFS: the registry serves only large
		// files by digest.
		return File{}, fmt.Errorf("%s is not a large (LFS) file, so it cannot be fetched by digest", r)
	}
	size, _ := strconv.ParseInt(resp.Header.Get("X-Linked-Size"), 10, 64)
	return File{Ref: r, Digest: "sha256:" + etag, Size: size, Commit: resp.Header.Get("X-Repo-Commit")}, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Kind is a media kind as the hub tags it.
type Kind string

const (
	KindImage Kind = "image"
	KindEdit  Kind = "edit"
	KindSTT   Kind = "stt"
	KindTTS   Kind = "tts"
	KindVideo Kind = "video"
)

// pipelineTags maps a kind to the hub's pipeline tag.
var pipelineTags = map[Kind]string{
	KindImage: "text-to-image",
	KindEdit:  "image-to-image",
	KindSTT:   "automatic-speech-recognition",
	KindTTS:   "text-to-speech",
	KindVideo: "text-to-video",
}

// Kinds lists the kinds Search takes.
func Kinds() []Kind { return []Kind{KindImage, KindEdit, KindSTT, KindTTS, KindVideo} }

// Repo is one search result.
type Repo struct {
	ID          string   `json:"id"`
	Downloads   int      `json:"downloads"`
	Likes       int      `json:"likes"`
	PipelineTag string   `json:"pipeline_tag"`
	Tags        []string `json:"tags"`
}

// Search lists the hub's repos for a kind, most downloaded first. Only GGUF
// repos are asked for unless gguf is false: that is what the engines read,
// besides the VAEs and whisper.cpp files a template names by path.
func Search(ctx context.Context, client *http.Client, kind Kind, query string, gguf bool, limit int) ([]Repo, error) {
	tag, ok := pipelineTags[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q (want one of %v)", kind, Kinds())
	}
	q := url.Values{"pipeline_tag": {tag}, "sort": {"downloads"}, "direction": {"-1"}, "limit": {strconv.Itoa(max(limit, 1))}}
	if query != "" {
		q.Set("search", query)
	}
	if gguf {
		q.Set("filter", "gguf")
	}
	var out []Repo
	if err := getJSON(ctx, client, Endpoint()+"/api/models?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RepoFile is one file of a repo.
type RepoFile struct {
	Path   string
	Size   int64
	Digest string // sha256:<hex> for a large file; empty for one kept in git
	Format string // gguf, safetensors, ggml (a whisper.cpp .bin), or other
}

// Ref is the reference that names the file.
func (f RepoFile) Ref(repo, rev string) Ref { return Ref{Repo: repo, Path: f.Path, Rev: rev} }

// Files lists the repo's weight files: GGUF, safetensors and whisper.cpp
// .bin. Docs, configs and tokenizers are left out.
func Files(ctx context.Context, client *http.Client, repo, rev string) ([]RepoFile, error) {
	if rev == "" {
		rev = "main"
	}
	var tree []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
		LFS  *struct {
			Oid string `json:"oid"`
		} `json:"lfs"`
	}
	u := Endpoint() + "/api/models/" + repo + "/tree/" + url.PathEscape(rev) + "?recursive=true"
	if err := getJSON(ctx, client, u, &tree); err != nil {
		return nil, err
	}
	var out []RepoFile
	for _, e := range tree {
		if e.Type != "file" {
			continue
		}
		format := FormatOf(e.Path)
		if format == "other" {
			continue
		}
		f := RepoFile{Path: e.Path, Size: e.Size, Format: format}
		if e.LFS != nil && sha256Hex.MatchString(e.LFS.Oid) {
			f.Digest = "sha256:" + e.LFS.Oid
		}
		out = append(out, f)
	}
	return out, nil
}

// FormatOf names a weight file's format by its extension.
func FormatOf(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".gguf":
		return "gguf"
	case ".safetensors":
		return "safetensors"
	case ".bin":
		if strings.HasPrefix(path.Base(p), "ggml-") {
			return "ggml"
		}
	}
	return "other"
}

func getJSON(ctx context.Context, client *http.Client, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Hugging Face answered %d for %s", resp.StatusCode, u)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
