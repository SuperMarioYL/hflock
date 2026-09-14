package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultModelScopeBase is the canonical ModelScope (阿里魔搭) host.
const DefaultModelScopeBase = "https://www.modelscope.cn"

// modelScopeRevision is ModelScope's default branch for model repositories.
const modelScopeRevision = "master"

// lfsForceThreshold mirrors the ModelScope SDK's
// UPLOAD_LFS_FORCE_THRESHOLD_BYTES: files at or above this size go through
// the LFS batch flow even when their suffix is not in lfsSuffixes.
const lfsForceThreshold = 1 << 20 // 1 MiB

// lfsSuffixes mirrors the ModelScope SDK's MODEL_LFS_SUFFIX list: file types
// the platform stores via LFS regardless of size.
var lfsSuffixes = map[string]bool{
	".7z": true, ".arrow": true, ".bin": true, ".bz2": true, ".ckpt": true,
	".ftz": true, ".gz": true, ".h5": true, ".joblib": true, ".mlmodel": true,
	".model": true, ".msgpack": true, ".npy": true, ".npz": true, ".onnx": true,
	".ot": true, ".parquet": true, ".pb": true, ".pickle": true, ".pkl": true,
	".pt": true, ".pth": true, ".rar": true, ".safetensors": true, ".tar": true,
	".tflite": true, ".tgz": true, ".wasm": true, ".xz": true, ".zip": true,
	".zst": true,
}

// ModelScopeTarget mirrors weights to ModelScope. The upload flow is the one
// ModelScope's own SDK (modelscope_hub) implements: ensure the repo exists,
// validate blobs via the LFS batch API, PUT each missing blob to its
// presigned URL, then create one commit carrying an action per file.
// Authentication uses both an Authorization bearer header and the
// m_session_id cookie, matching the SDK.
type ModelScopeTarget struct {
	Base   string // default DefaultModelScopeBase
	Token  string // ModelScope access token
	Client *http.Client
}

// Name returns the manifest mirror id prefix.
func (m *ModelScopeTarget) Name() string { return "modelscope" }

// MirrorID is the manifest record for a pinned repo.
func (m *ModelScopeTarget) MirrorID(repo string) string { return "modelscope:" + repo }

// RepoURL is the canonical human-facing mirror URL for a repo.
func (m *ModelScopeTarget) RepoURL(repo string) string {
	return strings.TrimRight(m.base(), "/") + "/models/" + repo
}

type msUploadFile struct {
	path   string // repo-relative path
	local  string // local cache path
	sha256 string
	size   int64
	lfs    bool
}

// UploadRepo uploads files (repo-relative path -> local path) to the mirror
// repo as one batch, creating the repo first when it does not exist yet.
// revision is the pinned source revision recorded as provenance; commits land
// on ModelScope's default branch (master), matching the SDK's behavior.
func (m *ModelScopeTarget) UploadRepo(ctx context.Context, repo, revision string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	if m.Token == "" {
		return fmt.Errorf("modelscope: access token required (set HFLOCK_MODELSCOPE_TOKEN or --modelscope-token)")
	}
	if err := m.ensureRepo(ctx, repo); err != nil {
		return err
	}

	metas, err := m.scanFiles(files)
	if err != nil {
		return err
	}

	// LFS batch validation: which blobs does the server already have?
	var objects []map[string]any
	for _, f := range metas {
		if f.lfs {
			objects = append(objects, map[string]any{"oid": f.sha256, "size": f.size})
		}
	}
	uploadURLs := map[string]string{} // sha256 -> presigned PUT href
	if len(objects) > 0 {
		payload, err := json.Marshal(map[string]any{"operation": "upload", "objects": objects})
		if err != nil {
			return err
		}
		resp, err := m.do(ctx, http.MethodPost, "/api/v1/repos/models/"+repo+"/info/lfs/objects/batch", payload)
		if err != nil {
			return fmt.Errorf("modelscope: lfs batch %s: %w", repo, err)
		}
		data, err := decodeEnvelope(resp)
		if err != nil {
			return fmt.Errorf("modelscope: lfs batch %s: %w", repo, err)
		}
		var batch struct {
			Objects []struct {
				Oid     string `json:"oid"`
				Actions *struct {
					Upload *struct {
						Href string `json:"href"`
					} `json:"upload"`
				} `json:"actions"`
			} `json:"objects"`
		}
		if err := json.Unmarshal(data, &batch); err != nil {
			return fmt.Errorf("modelscope: lfs batch %s: parse response: %w", repo, err)
		}
		for _, o := range batch.Objects {
			if o.Actions != nil && o.Actions.Upload != nil && o.Actions.Upload.Href != "" {
				uploadURLs[o.Oid] = o.Actions.Upload.Href
			}
		}
	}

	// Upload the blobs the server is missing.
	for _, f := range metas {
		if !f.lfs {
			continue
		}
		href, ok := uploadURLs[f.sha256]
		if !ok {
			continue // blob already on the server — reuse it
		}
		if err := m.putBlob(ctx, href, f); err != nil {
			return fmt.Errorf("modelscope: upload blob %s: %w", f.path, err)
		}
	}

	// One commit carrying an action per file (same action shapes the SDK's
	// _build_operation emits).
	actions := make([]map[string]any, 0, len(metas))
	for _, f := range metas {
		if f.lfs {
			actions = append(actions, map[string]any{
				"action": "create", "path": f.path, "type": "lfs",
				"size": f.size, "sha256": f.sha256, "content": "", "encoding": "",
			})
			continue
		}
		b, err := os.ReadFile(f.local)
		if err != nil {
			return fmt.Errorf("modelscope: read %s: %w", f.local, err)
		}
		actions = append(actions, map[string]any{
			"action": "create", "path": f.path, "type": "normal",
			"size": f.size, "sha256": "",
			"content": base64.StdEncoding.EncodeToString(b), "encoding": "base64",
		})
	}
	msg := "hflock: mirror " + repo
	if revision != "" {
		msg += "@" + revision
	}
	payload, err := json.Marshal(map[string]any{
		"commit_message": msg,
		"actions":        actions,
	})
	if err != nil {
		return err
	}
	resp, err := m.do(ctx, http.MethodPost, "/api/v1/repos/models/"+repo+"/commit/"+modelScopeRevision, payload)
	if err != nil {
		return fmt.Errorf("modelscope: commit %s: %w", repo, err)
	}
	if _, err := decodeEnvelope(resp); err != nil {
		return fmt.Errorf("modelscope: commit %s: %w", repo, err)
	}
	return nil
}

// scanFiles hashes each local file and classifies it LFS (suffix list or >=
// 1 MiB) vs normal (base64-embedded in the commit).
func (m *ModelScopeTarget) scanFiles(files map[string]string) ([]msUploadFile, error) {
	metas := make([]msUploadFile, 0, len(files))
	for path, local := range files {
		f, err := os.Open(local)
		if err != nil {
			return nil, fmt.Errorf("modelscope: open %s: %w", local, err)
		}
		h := sha256.New()
		size, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("modelscope: hash %s: %w", local, err)
		}
		metas = append(metas, msUploadFile{
			path:   path,
			local:  local,
			sha256: hex.EncodeToString(h.Sum(nil)),
			size:   size,
			lfs:    lfsSuffixes[strings.ToLower(filepath.Ext(path))] || size >= lfsForceThreshold,
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].path < metas[j].path })
	return metas, nil
}

// ensureRepo leaves an existing repo alone and creates a missing one with the
// same PascalCase body the ModelScope SDK sends (Path=owner, Name=name,
// Visibility=1 public, License).
func (m *ModelScopeTarget) ensureRepo(ctx context.Context, repo string) error {
	resp, err := m.do(ctx, http.MethodGet, "/api/v1/models/"+repo, nil)
	if err != nil {
		return fmt.Errorf("modelscope: check repo %s: %w", repo, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("modelscope: check repo %s: HTTP %d", repo, resp.StatusCode)
	}
	slash := strings.IndexByte(repo, '/')
	if slash <= 0 || slash == len(repo)-1 {
		return fmt.Errorf("modelscope: repo id %q must be owner/name", repo)
	}
	body, err := json.Marshal(map[string]any{
		"Path":       repo[:slash],
		"Name":       repo[slash+1:],
		"Visibility": 1, // public, the SDK's default
		"License":    "Apache-2.0",
	})
	if err != nil {
		return err
	}
	resp, err = m.do(ctx, http.MethodPost, "/api/v1/models", body)
	if err != nil {
		return fmt.Errorf("modelscope: create repo %s: %w", repo, err)
	}
	if _, err := decodeEnvelope(resp); err != nil {
		return fmt.Errorf("modelscope: create repo %s: %w", repo, err)
	}
	return nil
}

// putBlob streams the raw file bytes to the presigned URL with the same
// auth headers the SDK sends (the LFS domain may differ from the API host).
func (m *ModelScopeTarget) putBlob(ctx context.Context, href string, f msUploadFile) error {
	fh, err := os.Open(f.local)
	if err != nil {
		return err
	}
	defer fh.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, href, fh)
	if err != nil {
		return err
	}
	req.ContentLength = f.size
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("Cookie", "m_session_id="+m.Token)
	resp, err := m.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// do issues an authenticated JSON request against the ModelScope API.
func (m *ModelScopeTarget) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base()+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("Cookie", "m_session_id="+m.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return m.client().Do(req)
}

// decodeEnvelope unwraps ModelScope's standard legacy response shape
// {"Code": 200, "Message": "...", "Data": ...}: non-200 codes are errors,
// and Data is returned when present (otherwise the whole body). It consumes
// and closes the response body.
func decodeEnvelope(resp *http.Response) (json.RawMessage, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var env struct {
		Code    int             `json:"Code"`
		Message string          `json:"Message"`
		Data    json.RawMessage `json:"Data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if env.Code != 0 && env.Code != http.StatusOK {
		return nil, fmt.Errorf("code %d: %s", env.Code, env.Message)
	}
	if len(env.Data) > 0 {
		return env.Data, nil
	}
	return json.RawMessage(raw), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (m *ModelScopeTarget) base() string {
	if m.Base != "" {
		return strings.TrimRight(m.Base, "/")
	}
	return DefaultModelScopeBase
}

func (m *ModelScopeTarget) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return http.DefaultClient
}
