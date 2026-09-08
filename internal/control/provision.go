// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/MarcFryd/wagaStrim/internal/config"
)

type cameraLinks struct {
	Moblin string `json:"moblin"`
	WHIP   string `json:"whip"`
	WHEP   string `json:"whep"`
	Player string `json:"player"`
}

type cameraResponse struct {
	config.Ingest
	Links cameraLinks `json:"links"`
}

func (s *Server) cameraResponse(camera config.Ingest) cameraResponse {
	host := s.cfg.Host()
	if host == "" {
		host = "127.0.0.1"
	}
	moblin, player := s.cfg.SignalLinks(host)
	base := strings.TrimSuffix(player, "/player/")

	return cameraResponse{Ingest: camera, Links: cameraLinks{
		Moblin: moblin + camera.SenderKey,
		WHIP:   base + "/whip/" + camera.SenderKey,
		WHEP:   base + "/whep/" + camera.ReceiverKey,
		Player: player + camera.ReceiverKey,
	}}
}

func (s *Server) handleCreate(wri http.ResponseWriter, req *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	if err := decode(req, &body); err != nil {
		http.Error(wri, "expected a camera label", http.StatusBadRequest)

		return
	}
	body.Label = strings.TrimSpace(body.Label)
	if body.Label == "" || utf8.RuneCountInString(body.Label) > 48 {
		http.Error(wri, "label must contain 1 to 48 characters", http.StatusBadRequest)

		return
	}
	camera, err := s.cfg.AddIngest(body.Label)
	if err != nil {
		s.log.Warnf("create camera: %v", err)
		http.Error(wri, "could not save camera", http.StatusInternalServerError)

		return
	}
	s.respond(wri, http.StatusCreated, s.cameraResponse(camera))
}

func (s *Server) handleList(wri http.ResponseWriter, _ *http.Request) {
	cameras := s.cfg.List()
	result := make([]cameraResponse, 0, len(cameras))
	for _, camera := range cameras {
		result = append(result, s.cameraResponse(camera))
	}
	s.respond(wri, http.StatusOK, struct {
		Ingests []cameraResponse `json:"ingests"`
	}{Ingests: result})
}

func (s *Server) handleResetKey(wri http.ResponseWriter, req *http.Request) {
	camera, err := s.cfg.ResetSenderKey(req.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, config.ErrUnknownIngest) {
			status = http.StatusNotFound
		}
		http.Error(wri, "could not reset stream key", status)

		return
	}
	s.revoke(camera.ID)
	s.respond(wri, http.StatusOK, s.cameraResponse(camera))
}

func (s *Server) respond(wri http.ResponseWriter, status int, value any) {
	wri.Header().Set("Content-Type", "application/json")
	wri.WriteHeader(status)
	if err := json.NewEncoder(wri).Encode(value); err != nil {
		s.log.Warnf("control response: %v", err)
	}
}
