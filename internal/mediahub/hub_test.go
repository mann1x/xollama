package mediahub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ollama/ollama/types/xollama"
)

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"hf.co/leejet/FLUX.2-klein-4B-GGUF/flux-2-klein-4b-Q8_0.gguf": {"leejet/FLUX.2-klein-4B-GGUF", "flux-2-klein-4b-Q8_0.gguf", "main"},
		"huggingface.co/a/b/split_files/vae/ae.safetensors@v2":        {"a/b", "split_files/vae/ae.safetensors", "v2"},
		"https://huggingface.co/a/b/resolve/abc123/dir/x.gguf":        {"a/b", "dir/x.gguf", "abc123"},
		"https://huggingface.co/a/b/blob/main/x.gguf":                 {"a/b", "x.gguf", "main"},
	} {
		got, err := ParseRef(in)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"/srv/ml/x.gguf", "sha256:abc", "hf.co/a/b", "hf.co/a/b/../../etc/passwd", "hf.co/a/b/x.gguf@"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
	r := Ref{Repo: "a/b", Path: "x.gguf", Rev: "main"}
	if r.String() != "hf.co/a/b/x.gguf" || r.Registry() != "hf.co/a/b" {
		t.Fatalf("String %q Registry %q", r.String(), r.Registry())
	}
}

const sha = "868fe7b343cc8f3a19dbcfcafbc3d5f888802be3f89bd81b65b3621a066ce8f3"

func fakeHub(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/a/b/resolve/main/vae/flux2-vae.safetensors":
			w.Header().Set("X-Linked-Etag", `"`+sha+`"`)
			w.Header().Set("X-Linked-Size", "336211292")
			w.Header().Set("X-Repo-Commit", "5f52")
			w.Header().Set("Location", "https://cdn.example/blob")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/a/b/resolve/main/README.md":
			w.Header().Set("X-Linked-Etag", `"858fc6c847abae6509b6e2ec0d28b642ab9b227b"`)
			w.WriteHeader(http.StatusTemporaryRedirect)
		case r.URL.Path == "/api/models":
			json.NewEncoder(w).Encode([]Repo{{ID: "leejet/FLUX.2-klein-4B-GGUF", Downloads: 9}})
		case r.URL.Path == "/api/models/a/b/tree/main":
			fmt.Fprintf(w, `[{"type":"file","path":"x-Q4_0.gguf","size":10,"lfs":{"oid":"%s"}},
				{"type":"file","path":"README.md","size":1},
				{"type":"directory","path":"vae"},
				{"type":"file","path":"vae/ae.safetensors","size":5,"lfs":{"oid":"%s"}},
				{"type":"file","path":"ggml-base.bin","size":3,"lfs":{"oid":"%s"}},
				{"type":"file","path":"pytorch_model.bin","size":3}]`, sha, sha, sha)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HF_ENDPOINT", srv.URL)
	return &seen
}

func TestResolveReadsTheDigestWithoutDownloading(t *testing.T) {
	fakeHub(t)
	ctx := context.Background()
	got, err := Resolve(ctx, http.DefaultClient, Ref{Repo: "a/b", Path: "vae/flux2-vae.safetensors", Rev: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != "sha256:"+sha || got.Size != 336211292 || got.Commit != "5f52" {
		t.Fatalf("resolved %+v", got)
	}
	if _, err := Resolve(ctx, http.DefaultClient, Ref{Repo: "a/b", Path: "README.md", Rev: "main"}); err == nil || !strings.Contains(err.Error(), "not a large (LFS) file") {
		t.Fatalf("a git-kept file: err = %v", err)
	}
	if _, err := Resolve(ctx, http.DefaultClient, Ref{Repo: "a/b", Path: "nope.gguf", Rev: "main"}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("a missing file: err = %v", err)
	}
}

func TestSearchAsksTheHubForTheKindsTask(t *testing.T) {
	seen := fakeHub(t)
	got, err := Search(context.Background(), http.DefaultClient, KindTTS, "kokoro", true, 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("search = %v, %v", got, err)
	}
	q := (*seen)[0]
	for _, want := range []string{"pipeline_tag=text-to-speech", "search=kokoro", "filter=gguf", "limit=5", "sort=downloads"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %s lacks %s", q, want)
		}
	}
	if _, err := Search(context.Background(), http.DefaultClient, "music", "", true, 5); err == nil {
		t.Fatal("an unknown kind was searched")
	}
}

func TestFilesListsOnlyWeights(t *testing.T) {
	fakeHub(t)
	got, err := Files(context.Background(), http.DefaultClient, "a/b", "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got {
		names = append(names, f.Path+":"+f.Format)
		if f.Digest != "sha256:"+sha {
			t.Errorf("%s digest %q", f.Path, f.Digest)
		}
	}
	if strings.Join(names, ",") != "x-Q4_0.gguf:gguf,vae/ae.safetensors:safetensors,ggml-base.bin:ggml" {
		t.Fatalf("files = %v", names)
	}
}

func TestEveryCatalogEntryIsAValidTemplate(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Catalog() {
		if seen[e.ID] {
			t.Fatalf("duplicate id %s", e.ID)
		}
		seen[e.ID] = true
		for _, r := range e.Refs() {
			if _, err := ParseRef(r); err != nil {
				t.Errorf("%s: %v", e.ID, err)
			}
		}
		m := e.Media.Clone()
		m.MapComponents(func(string) string { return "sha256:" + strings.Repeat("a", 64) })
		if _, err := (&xollama.Config{Media: m}).Marshal(); err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
		kinds := strings.Join(e.Media.Kinds(), ",")
		if e.Kind != "mix" && kinds != e.Kind {
			t.Errorf("%s: kind %s, media carries %s", e.ID, e.Kind, kinds)
		}
	}
	if _, ok := Find("flux2-klein-4b"); !ok {
		t.Fatal("klein missing")
	}
	// Catalog hands out copies.
	c := Catalog()
	c[0].Media.Image.Defaults.Steps = 99
	if e, _ := Find(c[0].ID); e.Media.Image.Defaults.Steps == 99 {
		t.Fatal("editing a catalog entry changed the catalog")
	}
}

// TestCatalogResolvesLive resolves every reference against the real hub.
// Run with XOLLAMA_HF_LIVE=1.
func TestCatalogResolvesLive(t *testing.T) {
	if os.Getenv("XOLLAMA_HF_LIVE") == "" {
		t.Skip("set XOLLAMA_HF_LIVE=1 to resolve the catalog against huggingface.co")
	}
	for _, e := range Catalog() {
		for _, s := range e.Refs() {
			r, _ := ParseRef(s)
			f, err := Resolve(context.Background(), http.DefaultClient, r)
			if err != nil {
				t.Errorf("%s: %v", e.ID, err)
				continue
			}
			t.Logf("%-22s %s %d", e.ID, f.Digest[:19], f.Size)
		}
	}
}
