//go:build !windows

package orchestrator

// Non-Windows stub: the Windows kernel APIs are unavailable, so the sampler
// reports zero load. This keeps `go build ./...` working on non-Windows
// platforms without dragging in the windows-only x/sys/windows package.
type sysSampler struct{}

func (sysSampler) Load() (cpu, ram float64, err error) { return 0, 0, nil }

// NewSysSampler returns a ResourceSampler backed by Windows kernel APIs on
// Windows and a zero-reporting stub elsewhere.
func NewSysSampler() ResourceSampler { return sysSampler{} }
