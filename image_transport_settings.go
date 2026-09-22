package main

import (
	"errors"
	"net/http"
)

type imageTransportSettings struct {
	Mode    string `json:"mode"`
	Profile string `json:"profile"`
}

func normalizeImageTransportSettings(value imageTransportSettings) imageTransportSettings {
	if value.Mode == "" {
		value.Mode = "off"
	}
	if value.Profile == "" {
		value.Profile = "high"
	}
	return value
}

func validateImageTransportSettings(value imageTransportSettings) error {
	if value.Mode != "off" && value.Mode != "compress" && value.Mode != "upload" {
		return errors.New("Choose off, compress, or upload for large images.")
	}
	if value.Profile != "high" && value.Profile != "balanced" && value.Profile != "small" {
		return errors.New("Choose high, balanced, or small for image compression.")
	}
	return nil
}

// These choices apply to new requests without restarting the connection.
// Changing mode does not interrupt cleanup for earlier uploads.
func (a *app) imageTransportSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.mu.Lock()
		settings := normalizeImageTransportSettings(a.config.ImageTransport)
		a.mu.Unlock()
		jsonResponse(w, http.StatusOK, settings)
		return
	}
	if r.Method != http.MethodPut {
		jsonError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var input imageTransportSettings
	if !decodeBody(w, r, &input) {
		return
	}
	if err := validateImageTransportSettings(input); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.config
	cfg.ImageTransport = input
	if err := writeSettings(a.dir, cfg); err != nil {
		jsonError(w, http.StatusInternalServerError, "Could not save the image preference. Check the configuration folder permissions.")
		return
	}
	a.config = cfg
	jsonResponse(w, http.StatusOK, input)
}
