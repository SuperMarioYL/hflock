package mirror

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeHF returns an httptest server mimicking the two HF endpoints hflock
// uses — the file-tree API and the resolve download — plus an HFSource pointed
// at it. files maps "{repo}/resolve/{rev}/{file}" -> content.
func newFakeHF(t *testing.T, files map[string]string) (*httptest.Server, *HFSource) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/api/models/")
		idx := strings.Index(p, "/tree/")
		if idx < 0 {
			http.NotFound(w, r)
			return
		}
		repo := p[:idx]
		rev := p[idx+len("/tree/"):]
		prefix := repo + "/resolve/" + rev + "/"
		var tree []hfTreeEntry
		seen := map[string]bool{}
		for k := range files {
			if strings.HasPrefix(k, prefix) {
				name := strings.TrimPrefix(k, prefix)
				if !seen[name] {
					tree = append(tree, hfTreeEntry{Type: "file", Path: name, Size: int64(len(files[k]))})
					seen[name] = true
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[")) // minimal hand-built JSON avoids import-order flakes
		for i, e := range tree {
			if i > 0 {
				w.Write([]byte(","))
			}
			w.Write([]byte(`{"type":"file","path":"` + e.Path + `","size":` + itoa(e.Size) + `}`))
		}
		w.Write([]byte("]"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if content, ok := files[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("Content-Type", "application/octet-stream")
			io.WriteString(w, content)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	src := &HFSource{Base: srv.URL, Client: srv.Client()}
	t.Cleanup(srv.Close)
	return srv, src
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestListFiles_ExactNoGlob(t *testing.T) {
	_, src := newFakeHF(t, map[string]string{})
	got, err := src.ListFiles(context.Background(), "deepseek-ai/DeepSeek-V3", "v3.0",
		[]string{"config.json", "tokenizer.json"})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	want := []string{"config.json", "tokenizer.json"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, s := range got {
		if s != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestListFiles_GlobExpansion(t *testing.T) {
	files := map[string]string{
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json":            "{}",
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/model-00001.safetensors": "AAAA",
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/model-00002.safetensors": "BBBB",
	}
	_, src := newFakeHF(t, files)
	got, err := src.ListFiles(context.Background(), "deepseek-ai/DeepSeek-V3", "v3.0",
		[]string{"*.safetensors"})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 safetensors shards", got)
	}
	for _, g := range got {
		if !strings.HasSuffix(g, ".safetensors") {
			t.Fatalf("non-safetensors returned: %q", g)
		}
	}
}

// The real HF tree API paginates via Link rel="next" (default page size 1000);
// large sharded repos span multiple pages. Regression test for the v0.1.0
// defect where listTree read only the first page and silently dropped the
// rest — a "*.safetensors" pin then mirrored/hashed only page-1 shards.
func TestListFiles_PaginationFollowsLinkNext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/o/r/tree/main", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/api/models/o/r/tree/main?recursive=true&page=2>; rel="next"`, r.Host))
			w.Write([]byte(`[{"type":"file","path":"config.json","size":2},{"type":"file","path":"model-00001.safetensors","size":4}]`))
		case "2":
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/api/models/o/r/tree/main?recursive=true&page=3>; rel="next"`, r.Host))
			w.Write([]byte(`[{"type":"directory","path":"subdir"},{"type":"file","path":"model-00002.safetensors","size":4}]`))
		default: // page 3, no next — also exercises a relative Link target
			w.Header().Set("Link", `</api/models/o/r/tree/main?recursive=true&page=99>; rel="prev"`)
			w.Write([]byte(`[{"type":"file","path":"model-00003.safetensors","size":4}]`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	src := &HFSource{Base: srv.URL, Client: srv.Client()}

	got, err := src.ListFiles(context.Background(), "o/r", "main", []string{"*.safetensors"})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	want := []string{"model-00001.safetensors", "model-00002.safetensors", "model-00003.safetensors"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v — files past page 1 must not be silently dropped", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// nextLink must resolve relative rel="next" targets against the page URL.
func TestNextLink_RelativeTarget(t *testing.T) {
	got := nextLink(`</api/models/o/r/tree/main?page=2>; rel="next", </api/models/o/r/tree/main>; rel="first"`,
		"https://huggingface.co/api/models/o/r/tree/main?recursive=true")
	if got != "https://huggingface.co/api/models/o/r/tree/main?page=2" {
		t.Fatalf("nextLink = %q", got)
	}
	if nextLink(`</x>; rel="prev"`, "https://h") != "" {
		t.Fatalf("rel=prev must not be followed")
	}
	if nextLink("", "https://h") != "" {
		t.Fatalf("empty header must yield empty next")
	}
}

func TestListFiles_MixedExactAndGlob(t *testing.T) {
	files := map[string]string{
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json":             "C",
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/tokenizer.json":          "T",
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/model-00001.safetensors": "S",
	}
	_, src := newFakeHF(t, files)
	got, _ := src.ListFiles(context.Background(), "deepseek-ai/DeepSeek-V3", "v3.0",
		[]string{"config.json", "*.safetensors"})
	if got[0] != "config.json" {
		t.Fatalf("exact must lead: %v", got)
	}
	if len(got) != 2 || got[1] != "model-00001.safetensors" {
		t.Fatalf("got %v", got)
	}
}

// Regression test for the v0.1.0 defect where a glob matching zero files
// returned an empty slice with no error — verify then exited 0 and wrote an
// empty manifest, so a typo'd pattern silently disabled the CI gate.
func TestListFiles_GlobNoMatchFails(t *testing.T) {
	files := map[string]string{
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json": "C",
	}
	_, src := newFakeHF(t, files)
	_, err := src.ListFiles(context.Background(), "deepseek-ai/DeepSeek-V3", "v3.0",
		[]string{"*.safetenors"}) // typo'd pattern
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("err = %v, want 'matched no files' naming the pattern", err)
	}
	if !strings.Contains(err.Error(), "*.safetenors") || !strings.Contains(err.Error(), "deepseek-ai/DeepSeek-V3@v3.0") {
		t.Fatalf("error must name pattern and repo@revision: %v", err)
	}
	// a matching pattern in the same lockfile still succeeds (exact + glob mix)
	got, err := src.ListFiles(context.Background(), "deepseek-ai/DeepSeek-V3", "v3.0",
		[]string{"config.json", "*.json"})
	if err != nil || len(got) != 1 || got[0] != "config.json" {
		t.Fatalf("got %v, err %v", got, err)
	}
}

func TestDownload_OK(t *testing.T) {
	files := map[string]string{"deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json": "hello-world"}
	_, src := newFakeHF(t, files)
	var buf strings.Builder
	start, n, err := src.Download(context.Background(), "deepseek-ai/DeepSeek-V3", "v3.0", "config.json", 0, &buf)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if buf.String() != "hello-world" {
		t.Fatalf("body = %q", buf.String())
	}
	if n != int64(len("hello-world")) || start != 0 {
		t.Fatalf("start = %d, n = %d", start, n)
	}
}

func TestDownload_404(t *testing.T) {
	_, src := newFakeHF(t, map[string]string{})
	var buf strings.Builder
	_, _, err := src.Download(context.Background(), "deepseek-ai/DeepSeek-V3", "v3.0", "nope.json", 0, &buf)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v, want HTTP 404", err)
	}
}
