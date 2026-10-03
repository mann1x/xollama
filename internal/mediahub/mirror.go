package mediahub

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// A mirror is a local directory of Hugging Face files laid out as
// <owner>/<repo>/<path>, the layout huggingface-cli's --local-dir per repo
// gives. Media models are gigabytes; a mirror keeps one copy per machine, so a
// test store, a scratch server or a second host is filled from disk rather
// than downloaded again.
//
// A file is trusted by its sha256, never by its name. Once hashed, the digest
// is kept beside the file as <file>.sha256 together with its size and
// modification time, so the next check is a stat, and a file replaced since
// is hashed again.

// MirrorPath is where r lives in the mirror dir.
func MirrorPath(dir string, r Ref) string {
	return filepath.Join(dir, filepath.FromSlash(r.Repo), filepath.FromSlash(r.Path))
}

// Mirrored returns the mirror's copy of f, when the mirror has one whose
// sha256 is f's.
func Mirrored(dir string, f File) (string, bool) {
	if dir == "" {
		return "", false
	}
	p := MirrorPath(dir, f.Ref)
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() || (f.Size > 0 && fi.Size() != f.Size) {
		return "", false
	}
	if d, ok := sidecar(p, fi); ok {
		return p, d == f.Digest
	}
	d, err := hashTo(p)
	if err != nil {
		return "", false
	}
	return p, d == f.Digest
}

// sidecar reads the digest recorded for the file as it is now.
func sidecar(p string, fi os.FileInfo) (string, bool) {
	b, err := os.ReadFile(p + ".sha256")
	if err != nil {
		return "", false
	}
	var d string
	var size, mtime int64
	if _, err := fmt.Sscanf(string(b), "%s %d %d", &d, &size, &mtime); err != nil ||
		size != fi.Size() || mtime != fi.ModTime().UnixNano() {
		return "", false
	}
	return d, true
}

// record writes the sidecar. A mirror that cannot be written still answers;
// it only hashes again next time.
func record(p, digest string) {
	if fi, err := os.Stat(p); err == nil {
		_ = os.WriteFile(p+".sha256", fmt.Appendf(nil, "%s %d %d\n", digest, fi.Size(), fi.ModTime().UnixNano()), 0o644)
	}
}

// hashTo hashes a file and records the digest beside it.
func hashTo(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	d := fmt.Sprintf("sha256:%x", h.Sum(nil))
	record(p, d)
	return d, nil
}

// Fetch puts f in the mirror, unless a copy with its sha256 is already
// there, and returns the path. The download goes to <file>.part and is
// renamed only once its sha256 is f's, so an interrupted or wrong download
// never passes for the file. progress, when set, is told the bytes so far.
func Fetch(ctx context.Context, client *http.Client, dir string, f File, progress func(done, total int64)) (string, bool, error) {
	if p, ok := Mirrored(dir, f); ok {
		return p, false, nil
	}
	p := MirrorPath(dir, f.Ref)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", false, err
	}
	rev := f.Commit
	if rev == "" {
		rev = f.Rev
	}
	u := Endpoint() + "/" + f.Repo + "/resolve/" + url.PathEscape(rev) + "/" + escapePath(f.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false, err
	}
	if tok := os.Getenv("HF_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("%s: Hugging Face answered %d", f.Ref, resp.StatusCode)
	}

	part := p + ".part"
	out, err := os.Create(part)
	if err != nil {
		return "", false, err
	}
	h := sha256.New()
	w := io.MultiWriter(out, h)
	var done int64
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(part)
				return "", false, werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, f.Size)
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(part)
			return "", false, fmt.Errorf("%s: %w", f.Ref, rerr)
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return "", false, err
	}
	if d := fmt.Sprintf("sha256:%x", h.Sum(nil)); d != f.Digest {
		os.Remove(part)
		return "", false, fmt.Errorf("%s: downloaded %s, the hub names %s", f.Ref, short(d), short(f.Digest))
	}
	if err := os.Rename(part, p); err != nil {
		return "", false, err
	}
	record(p, f.Digest)
	return p, true, nil
}

func short(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}
