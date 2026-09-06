package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"draw/internal/model"
)

// apiServer holds request-scoped collaborators.
type apiServer struct {
	deps        *Deps
	sm          *sessionsManager
	sse         *SSEHandler
	buildResult func() model.ResultEnvelope
}

// NewServer builds the API server from Deps. ctx is the server-lifetime
// context (cancelled on shutdown). Research Run loops spawned by
// sessionsManager.Submit are derived from ctx — NOT from a per-request
// context — so a master.Run started by POST /sessions survives the handler
// returning and SSE can observe terminal state.
func NewServer(deps *Deps, ctx context.Context) *apiServer {
	sm := newSessionsManager(deps, ctx)
	buildResult := func() model.ResultEnvelope {
		return deps.ReportBuilder.Build(deps.Master.State())
	}
	sse := NewSSEHandler(deps.Master, buildResult)
	return &apiServer{deps: deps, sm: sm, sse: sse, buildResult: buildResult}
}

// Serve mounts routes and blocks on ListenAndServe until ctx is cancelled.
func Serve(ctx context.Context, addr string, deps *Deps, logger *slog.Logger) error {
	s := NewServer(deps, ctx)
	mux := http.NewServeMux()
	s.routes(mux)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if logger != nil {
		logger.Info("api server listening", "addr", addr, "admin_enabled", deps.Auth.AdminEnabled())
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// sessionsManager serializes the single ACTIVE session (reqding §0.3 Cor.6):
// submitting a new intent stops any in-flight Run first. Run loops are driven
// by srvCtx (the server-lifetime context), never by a request context.
type sessionsManager struct {
	deps      *Deps
	mu        sync.Mutex
	sessionID model.SessionID
	cancel    context.CancelFunc
	done      chan struct{}
	running   bool
	srvCtx    context.Context
}

func newSessionsManager(deps *Deps, ctx context.Context) *sessionsManager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &sessionsManager{deps: deps, srvCtx: ctx}
}

func (sm *sessionsManager) Submit(req model.IntentRequest) (model.SessionID, error) {
	sm.mu.Lock()
	if sm.running {
		if sm.cancel != nil {
			sm.cancel()
		}
		done := sm.done
		sm.mu.Unlock()
		<-done
		sm.mu.Lock()
	}
	sid, err := sm.deps.Master.SubmitIntent(req)
	if err != nil {
		sm.mu.Unlock()
		return "", err
	}
	runCtx, cancel := context.WithCancel(sm.srvCtx)
	sm.sessionID = sid
	sm.cancel = cancel
	sm.done = make(chan struct{})
	sm.running = true
	sm.mu.Unlock()
	go func() {
		defer close(sm.done)
		runErr := sm.deps.Master.Run(runCtx)
		_ = runErr
		// Archive the session once it reaches a terminal state (SSE terminal
		// report or ctx cancellation from POST /stop). Best-effort: a nil
		// SessionStore is a no-op.
		sm.archiveTerminal(sid)
	}()
	return sid, nil
}

func (sm *sessionsManager) Stop() error {
	sm.mu.Lock()
	if !sm.running {
		sm.mu.Unlock()
		return nil
	}
	sid := sm.sessionID
	done := sm.done
	sm.running = false
	sm.mu.Unlock()
	err := sm.deps.Master.Stop()
	<-done
	// POST /sessions/{id}/stop reaches terminal here — archive for recovery.
	sm.archiveTerminal(sid)
	return err
}

// archiveTerminal persists the session as archived when its run completes or
// is stopped. It is nil-safe (no-op when SessionStore is unconfigured) and
// never returns an error that would alter the Run/stop control flow.
func (sm *sessionsManager) archiveTerminal(sid model.SessionID) {
	if sm.deps == nil || sm.deps.SessionStore == nil {
		return
	}
	_ = sm.deps.SessionStore.Archive(sid)
}

func (sm *sessionsManager) IsActive(id model.SessionID) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.running && sm.sessionID == id
}
