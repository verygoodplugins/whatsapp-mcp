package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
)

//go:embed admin/* admin/i18n/*
var adminAssets embed.FS

const maxAdminPermissionUpdates = 1000

func registerAdminUI(
	mux *http.ServeMux,
	client *whatsmeow.Client,
	messageStore *MessageStore,
	port int,
	adminToken string,
) {
	if adminToken == "" {
		return
	}

	allowedHosts := buildAllowedHosts(port)
	sessions := newAdminSessionManager(adminToken)

	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		setAdminSecurityHeaders(w)
		if !hostAllowed(r.Host, allowedHosts) {
			http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/admin" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})

	assetRoot, err := fs.Sub(adminAssets, "admin")
	if err != nil {
		panic("embedded admin assets are unavailable: " + err.Error())
	}
	fileServer := http.StripPrefix("/admin/", http.FileServer(http.FS(assetRoot)))
	mux.Handle("/admin/", adminStaticHandler(allowedHosts, fileServer))

	mux.HandleFunc("/admin/login", sessions.loginHandler(allowedHosts))
	mux.HandleFunc("/admin/logout", sessions.logoutHandler(allowedHosts))
	mux.HandleFunc("/api/admin/chats", sessions.withSession(
		allowedHosts,
		adminChatsHandler(client, messageStore),
	))
	mux.HandleFunc("/api/admin/permissions", sessions.withSession(
		allowedHosts,
		adminPermissionsHandler(messageStore),
	))
	mux.HandleFunc("/api/admin/settings", sessions.withSession(
		allowedHosts,
		adminSettingsHandler(messageStore),
	))
}

func adminStaticHandler(allowedHosts map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAdminSecurityHeaders(w)
		if !hostAllowed(r.Host, allowedHosts) {
			http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func adminChatsHandler(client *whatsmeow.Client, messageStore *MessageStore) http.HandlerFunc {
	type response struct {
		Connected bool              `json:"connected"`
		Chats     []MCPAdminChat    `json:"chats"`
		Settings  MCPAccessSettings `json:"settings"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		chats, err := messageStore.ListMCPAccessChats()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not load chat permissions",
			})
			return
		}
		settings, err := messageStore.GetMCPAccessSettings()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Could not load access settings",
			})
			return
		}
		connected := client != nil && client.IsConnected()
		writeJSON(w, http.StatusOK, response{
			Connected: connected,
			Chats:     chats,
			Settings:  settings,
		})
	}
}

func adminPermissionsHandler(messageStore *MessageStore) http.HandlerFunc {
	type request struct {
		Updates []MCPChatPermissionUpdate `json:"updates"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var req request
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}
		if err := ensureJSONBodyConsumed(decoder); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}
		if len(req.Updates) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "At least one update is required"})
			return
		}
		if len(req.Updates) > maxAdminPermissionUpdates {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": "Too many permission updates",
			})
			return
		}

		if err := messageStore.UpdateMCPChatPermissions(req.Updates, time.Now().UTC()); err != nil {
			status := http.StatusInternalServerError
			message := "Could not update chat permissions"
			if errors.Is(err, ErrMCPPermissionConflict) {
				status = http.StatusConflict
				message = "Chat permissions changed; refresh the panel and try again"
			} else if errors.Is(err, ErrMCPChatNotFound) || errors.Is(err, ErrMCPDuplicateChatJID) {
				status = http.StatusBadRequest
				message = err.Error()
			}
			writeJSON(w, status, map[string]string{"error": message})
			return
		}

		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func adminSettingsHandler(messageStore *MessageStore) http.HandlerFunc {
	type request struct {
		AllowStartNewConversations *bool  `json:"allow_start_new_conversations"`
		ExpectedRevision           *int64 `json:"expected_revision"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var req request
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}
		if err := ensureJSONBodyConsumed(decoder); err != nil ||
			req.AllowStartNewConversations == nil ||
			req.ExpectedRevision == nil ||
			*req.ExpectedRevision < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}

		err := messageStore.UpdateMCPAccessSettings(
			*req.AllowStartNewConversations,
			*req.ExpectedRevision,
			time.Now().UTC(),
		)
		if err != nil {
			status := http.StatusInternalServerError
			message := "Could not update access settings"
			if errors.Is(err, ErrMCPSettingsConflict) {
				status = http.StatusConflict
				message = "Access settings changed; refresh the panel and try again"
			}
			writeJSON(w, status, map[string]string{"error": message})
			return
		}

		settings, err := messageStore.GetMCPAccessSettings()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Settings updated but could not be reloaded",
			})
			return
		}
		writeJSON(w, http.StatusOK, settings)
	}
}

func ensureJSONBodyConsumed(decoder *json.Decoder) error {
	var extra interface{}
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}
