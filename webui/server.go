package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"telemetry-handler/config"
	"telemetry-handler/forza"
)

//go:embed static/*
var staticFiles embed.FS

type Runtime interface {
	Config() config.Config
	LatestTelemetry() TelemetrySnapshot
	ApplyConfig(config.Config) error
	SaveConfig(config.Config) error
	PreviewMoza(config.Moza) error
}

type TelemetrySnapshot struct {
	Telemetry  forza.Telemetry `json:"telemetry"`
	ReceivedAt time.Time       `json:"received_at"`
	Available  bool            `json:"available"`
}

type Server struct {
	runtime Runtime
	handler http.Handler
}

func NewServer(runtime Runtime) *Server {
	server := &Server{runtime: runtime}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/telemetry", server.handleTelemetry)
	mux.HandleFunc("GET /api/config", server.handleConfig)
	mux.HandleFunc("PUT /api/config/apply", server.handleApplyConfig)
	mux.HandleFunc("PUT /api/config/save", server.handleSaveConfig)
	mux.HandleFunc("POST /api/moza/preview", server.handleMozaPreview)
	mux.Handle("/", server.staticHandler())

	server.handler = mux
	return server
}

func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return ctx.Err()
	case err := <-errc:
		return err
	}
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runtime.LatestTelemetry())
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runtime.Config())
}

func (s *Server) handleApplyConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := readJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.runtime.ApplyConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runtime.Config())
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := readJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.runtime.SaveConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleMozaPreview(w http.ResponseWriter, r *http.Request) {
	var cfg config.Moza
	if err := readJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.runtime.PreviewMoza(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "previewed"})
}

func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(fmt.Sprintf("webui static files: %v", err))
	}
	return http.FileServer(http.FS(sub))
}

func readJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
