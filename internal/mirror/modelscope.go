package mirror

import (
	"context"
	"fmt"
	"strings"
)

// DefaultModelScopeBase is the canonical ModelScope (阿里魔搭) host.
const DefaultModelScopeBase = "https://www.modelscope.cn"

// ModelScopeTarget mirrors weights to ModelScope. As with GiteeTarget, m1
// records no uploads; Upload ships in m2 sync.
type ModelScopeTarget struct {
	Base  string // default DefaultModelScopeBase
	Token string // upload credential; m2
}

// Name returns the manifest mirror id prefix.
func (m *ModelScopeTarget) Name() string { return "modelscope" }

// MirrorID is the manifest record for a pinned repo.
func (m *ModelScopeTarget) MirrorID(repo string) string { return "modelscope:" + repo }

// RepoURL is the canonical human-facing mirror URL for a repo.
func (m *ModelScopeTarget) RepoURL(repo string) string {
	return strings.TrimRight(m.base(), "/") + "/models/" + repo
}

// Upload copies a local weight file to ModelScope. m2.
func (m *ModelScopeTarget) Upload(_ context.Context, _, _, _, _ string) error {
	return fmt.Errorf("%s: %w", m.Name(), ErrMirrorNotImplemented)
}

func (m *ModelScopeTarget) base() string {
	if m.Base != "" {
		return m.Base
	}
	return DefaultModelScopeBase
}
