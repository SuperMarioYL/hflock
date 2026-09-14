package mirror

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// msMock is a mock ModelScope server implementing the endpoints the
// ModelScopeTarget drives: repo check/create, the LFS batch validation, the
// presigned blob PUT, and the commit. It records every request for assertions.
type msMock struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex // guards the recorded fields below

	getRepoStatus int // what GET /api/v1/models/{repo} answers; default 200
	createBodies  []map[string]any
	batchBodies   []map[string]any
	putBodies     map[string][]byte // oid -> received bytes
	putAuths      []string
	commitBodies  []map[string]any
}

func newMSMock(t *testing.T, getRepoStatus int) *msMock {
	t.Helper()
	m := &msMock{t: t, getRepoStatus: getRepoStatus, putBodies: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/models/o/r", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(m.getRepoStatus)
			io.WriteString(w, `{"Code":200,"Data":{"Name":"r"}}`)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("create body: %v", err)
		}
		m.mu.Lock()
		m.createBodies = append(m.createBodies, body)
		m.mu.Unlock()
		io.WriteString(w, `{"Code":200,"Data":{"Name":"r"}}`)
	})
	mux.HandleFunc("/api/v1/repos/models/o/r/info/lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("Cookie") == "" {
			t.Errorf("batch: missing auth headers (Authorization/Cookie)")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("batch body: %v", err)
		}
		m.mu.Lock()
		m.batchBodies = append(m.batchBodies, body)
		m.mu.Unlock()
		// every requested oid gets a presigned upload URL
		objects := body["objects"].([]any)
		out := make([]map[string]any, 0, len(objects))
		for _, o := range objects {
			oid := o.(map[string]any)["oid"].(string)
			out = append(out, map[string]any{
				"oid": oid,
				"actions": map[string]any{
					"upload": map[string]any{"href": m.srv.URL + "/presigned/" + oid},
				},
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"Code": 200, "Data": map[string]any{"objects": out}})
	})
	mux.HandleFunc("/presigned/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		m.mu.Lock()
		m.putAuths = append(m.putAuths, r.Header.Get("Authorization")+"|"+r.Header.Get("Cookie"))
		m.mu.Unlock()
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("presigned read: %v", err)
		}
		oid := filepath.Base(r.URL.Path)
		m.mu.Lock()
		m.putBodies[oid] = b
		m.mu.Unlock()
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("/api/v1/repos/models/o/r/commit/master", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("commit body: %v", err)
		}
		m.mu.Lock()
		m.commitBodies = append(m.commitBodies, body)
		m.mu.Unlock()
		io.WriteString(w, `{"Code":200,"Data":{"CommitId":"c1"}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func writeFileT(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestModelScopeTarget_Metadata(t *testing.T) {
	m := &ModelScopeTarget{}
	if m.Name() != "modelscope" {
		t.Fatalf("name = %q", m.Name())
	}
	if m.MirrorID("deepseek-ai/DeepSeek-V3") != "modelscope:deepseek-ai/DeepSeek-V3" {
		t.Fatalf("mirrorid = %q", m.MirrorID("deepseek-ai/DeepSeek-V3"))
	}
	if m.RepoURL("deepseek-ai/DeepSeek-V3") != DefaultModelScopeBase+"/models/deepseek-ai/DeepSeek-V3" {
		t.Fatalf("repo url = %q", m.RepoURL("deepseek-ai/DeepSeek-V3"))
	}
}

func TestModelScope_UploadRepoRequiresToken(t *testing.T) {
	m := &ModelScopeTarget{}
	err := m.UploadRepo(context.Background(), "o/r", "", map[string]string{"config.json": "/tmp/x"})
	if err == nil || !strings.Contains(err.Error(), "HFLOCK_MODELSCOPE_TOKEN") {
		t.Fatalf("err = %v, want token-required error", err)
	}
}

func TestModelScope_UploadRepoFullFlow(t *testing.T) {
	mock := newMSMock(t, http.StatusOK)
	dir := t.TempDir()
	safetensors := writeFileT(t, filepath.Join(dir, "model-00001-of-00002.safetensors"), "LFS-BYTES")
	config := writeFileT(t, filepath.Join(dir, "config.json"), `{"k":1}`)

	m := &ModelScopeTarget{Base: mock.srv.URL, Token: "tok-1", Client: mock.srv.Client()}
	if err := m.UploadRepo(context.Background(), "o/r", "", map[string]string{
		"model-00001-of-00002.safetensors": safetensors,
		"config.json":                      config,
	}); err != nil {
		t.Fatalf("UploadRepo: %v", err)
	}

	// no repo creation happened (repo existed)
	if len(mock.createBodies) != 0 {
		t.Fatalf("repo must not be recreated when it exists: %v", mock.createBodies)
	}
	// the LFS batch carried the safetensors blob only
	if len(mock.batchBodies) != 1 {
		t.Fatalf("batch calls = %d, want 1", len(mock.batchBodies))
	}
	batch := mock.batchBodies[0]
	if batch["operation"] != "upload" {
		t.Fatalf("operation = %v", batch["operation"])
	}
	objs := batch["objects"].([]any)
	if len(objs) != 1 {
		t.Fatalf("objects = %v, want only the lfs file", objs)
	}
	// the presigned PUT received the exact bytes with both auth headers
	if len(mock.putBodies) != 1 {
		t.Fatalf("puts = %v", mock.putBodies)
	}
	for _, b := range mock.putBodies {
		if string(b) != "LFS-BYTES" {
			t.Fatalf("put bytes = %q", b)
		}
	}
	if len(mock.putAuths) != 1 || mock.putAuths[0] != "Bearer tok-1|m_session_id=tok-1" {
		t.Fatalf("put auth = %v", mock.putAuths)
	}
	// one commit with the right action shapes
	if len(mock.commitBodies) != 1 {
		t.Fatalf("commit calls = %d, want 1", len(mock.commitBodies))
	}
	commit := mock.commitBodies[0]
	actions := commit["actions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("actions = %v", actions)
	}
	byType := map[string]map[string]any{}
	for _, a := range actions {
		am := a.(map[string]any)
		byType[am["type"].(string)] = am
	}
	lfs := byType["lfs"]
	if lfs["path"] != "model-00001-of-00002.safetensors" || lfs["sha256"] == "" || lfs["content"] != "" || lfs["encoding"] != "" {
		t.Fatalf("lfs action = %v", lfs)
	}
	if lfs["size"].(float64) != float64(len("LFS-BYTES")) {
		t.Fatalf("lfs size = %v", lfs["size"])
	}
	normal := byType["normal"]
	if normal["path"] != "config.json" || normal["sha256"] != "" || normal["encoding"] != "base64" {
		t.Fatalf("normal action = %v", normal)
	}
	decoded, err := base64.StdEncoding.DecodeString(normal["content"].(string))
	if err != nil || string(decoded) != `{"k":1}` {
		t.Fatalf("normal content = %q err %v", normal["content"], err)
	}
	if commit["commit_message"] != "hflock: mirror o/r" {
		t.Fatalf("commit_message = %v", commit["commit_message"])
	}
}

func TestModelScope_UploadRepoCreatesMissingRepo(t *testing.T) {
	mock := newMSMock(t, http.StatusNotFound)
	dir := t.TempDir()
	config := writeFileT(t, filepath.Join(dir, "config.json"), "C")

	m := &ModelScopeTarget{Base: mock.srv.URL, Token: "tok-1", Client: mock.srv.Client()}
	if err := m.UploadRepo(context.Background(), "o/r", "", map[string]string{"config.json": config}); err != nil {
		t.Fatalf("UploadRepo: %v", err)
	}
	if len(mock.createBodies) != 1 {
		t.Fatalf("create calls = %d, want 1", len(mock.createBodies))
	}
	body := mock.createBodies[0]
	// the PascalCase body the ModelScope SDK sends
	if body["Path"] != "o" || body["Name"] != "r" || body["Visibility"] != float64(1) || body["License"] != "Apache-2.0" {
		t.Fatalf("create body = %v", body)
	}
	// small json file never enters the LFS batch
	if len(mock.batchBodies) != 0 {
		t.Fatalf("no lfs batch expected for normal files: %v", mock.batchBodies)
	}
}
