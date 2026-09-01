package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type chatRunResponse struct {
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

type createChatSessionRequest struct {
	AppID     string `json:"app_id"`
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
}

type createChannelBindingRequest struct {
	Channel          string `json:"channel"`
	AppID            string `json:"app_id"`
	ConversationType string `json:"conversation_type"`
	ConversationID   string `json:"external_conversation_id"`
	UserID           string `json:"external_user_id"`
	SessionID        string `json:"session_id"`
	Secret           string `json:"secret,omitempty"`
}

type sendChatMessageRequest struct {
	Input string `json:"input"`
}

type cancelChatRunRequest struct {
	RequestID string `json:"request_id"`
}

func (h *AdminHandler) handleChannelBindings(w http.ResponseWriter, r *http.Request) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"items": h.channels.ListBindings(tenant.TenantID)})
	case http.MethodPost:
		if !canOperate(tenant.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
			return
		}
		var request createChannelBindingRequest
		if err := decodeStrict(r, &request); err != nil || !validResourceID(request.AppID) || request.ConversationID == "" || request.UserID == "" {
			writeError(w, http.StatusBadRequest, "invalid_channel_binding", "channel, app, conversation, and user identifiers are required")
			return
		}
		if _, exists := h.platform.app(tenant.TenantID, request.AppID); !exists {
			writeError(w, http.StatusNotFound, "agent_app_not_found", "Agent App was not found")
			return
		}
		binding, err := h.channels.CreateBinding(tenant, request)
		if errors.Is(err, ErrDuplicateEvent) {
			writeError(w, http.StatusConflict, "channel_binding_exists", "channel binding already exists")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_channel_binding", "channel binding is invalid")
			return
		}
		store, release, err := h.acquireStore(tenant.TenantID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
			return
		}
		defer release()
		if err := h.appendChatEvent(r.Context(), store, tenant.TenantID, binding.SessionID, "session-created", "session.created", map[string]string{
			"app_id": binding.AppID, "channel": binding.Channel, "user_id": binding.UserID, "conversation_type": binding.ConversationType,
		}); err != nil {
			writeError(w, http.StatusServiceUnavailable, "storage_error", "session event could not be persisted")
			return
		}
		if binding.Channel != ChannelMock {
			binding.Secret = ""
		}
		writeJSON(w, http.StatusCreated, binding)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
	}
}

func (h *AdminHandler) handleChannelBindingResource(w http.ResponseWriter, r *http.Request, id string) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	if id == "" || !canOperate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var request struct {
			Enabled *bool  `json:"enabled"`
			Secret  string `json:"secret"`
		}
		if err := decodeStrict(r, &request); err != nil || (request.Enabled == nil && strings.TrimSpace(request.Secret) == "") {
			writeError(w, http.StatusBadRequest, "invalid_channel_binding", "enabled or secret is required")
			return
		}
		var binding ChannelBinding
		var err error
		if request.Enabled != nil {
			binding, err = h.channels.UpdateBinding(tenant.TenantID, id, *request.Enabled)
		} else {
			binding, err = h.channels.ReplaceSecret(tenant.TenantID, id, request.Secret)
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel_binding_not_found", "channel binding was not found")
			return
		}
		writeJSON(w, http.StatusOK, binding)
	case http.MethodDelete:
		if err := h.channels.DeleteBinding(tenant.TenantID, id); errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel_binding_not_found", "channel binding was not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be PATCH or DELETE")
	}
}

func (h *AdminHandler) handleMockChannelCallback(w http.ResponseWriter, r *http.Request) {
	if _, ok := trustedTenant(r); !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	h.handleProviderChannelCallback(w, r, ChannelMock)
}

func (h *AdminHandler) handleProviderChannelCallback(w http.ResponseWriter, r *http.Request, channel string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_channel_callback", "callback body is invalid")
		return
	}
	var envelope struct {
		BindingID string `json:"binding_id"`
	}
	_ = json.Unmarshal(body, &envelope)
	if envelope.BindingID == "" {
		envelope.BindingID = r.URL.Query().Get("binding_id")
	}
	if envelope.BindingID == "" {
		envelope.BindingID = r.Header.Get("X-Channel-Binding-ID")
	}
	if envelope.BindingID == "" {
		writeError(w, http.StatusBadRequest, "invalid_channel_callback", "binding id is required")
		return
	}
	callback := ChannelCallback{
		Channel: channel, BindingID: envelope.BindingID, Body: body, Signature: callbackSignature(r, channel),
		Timestamp: r.URL.Query().Get("timestamp"), Nonce: r.URL.Query().Get("nonce"),
	}
	var binding ChannelBinding
	var message ChannelMessage
	if channel == ChannelMock {
		tenant, ok := trustedTenant(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
			return
		}
		binding, message, err = h.channels.Receive(r.Context(), tenant.TenantID, callback)
	} else {
		binding, message, err = h.channels.ReceiveExternal(r.Context(), callback)
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "channel_binding_not_found", "channel binding was not found")
		return
	}
	if err != nil {
		writeChannelError(w, err)
		return
	}
	tenant := TenantContext{TenantID: binding.TenantID, UserID: message.UserID, Role: RoleOperator}
	result, err := h.startChatRun(chatRunOptions{
		tenant: tenant, appID: binding.AppID, sessionID: binding.SessionID, input: message.Text,
		requestID: "channel-" + message.MessageID, userID: message.UserID, binding: &binding,
	})
	if err != nil {
		writeChatStartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func callbackSignature(r *http.Request, channel string) string {
	switch channel {
	case ChannelMock:
		return r.Header.Get("X-Mock-Signature")
	case ChannelEnterpriseWeChat:
		return r.URL.Query().Get("msg_signature")
	case ChannelTelegram:
		return r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	default:
		return ""
	}
}

func (h *AdminHandler) handleMockFaults(w http.ResponseWriter, r *http.Request) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.channels.MockFaults(tenant.TenantID, r.URL.Query().Get("session_id")))
	case http.MethodPost:
		if !canOperate(tenant.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
			return
		}
		var request struct {
			Scenario  string `json:"scenario"`
			SessionID string `json:"session_id"`
		}
		if err := decodeStrict(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_mock_fault", "scenario is required")
			return
		}
		config, err := h.channels.ConfigureMockFaults(tenant.TenantID, request.SessionID, request.Scenario)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_mock_fault", "scenario is invalid")
			return
		}
		writeJSON(w, http.StatusOK, config)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or POST")
	}
}

func (h *AdminHandler) handleChatSessionResource(w http.ResponseWriter, r *http.Request, parts []string) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "development identity is required")
		return
	}
	if len(parts) == 0 {
		h.handleChatSessions(w, r, tenant)
		return
	}
	sessionID := parts[0]
	if len(parts) == 2 {
		switch parts[1] {
		case "events":
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
				return
			}
			h.writeChatEvents(w, r, tenant, sessionID)
			return
		case "messages":
			h.handleSendChatMessage(w, r, tenant, sessionID)
			return
		case "cancel":
			h.handleCancelChatRun(w, r, tenant, sessionID)
			return
		case "stream":
			h.handleChatStream(w, r, tenant, sessionID)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "resource was not found")
}

func (h *AdminHandler) handleChatSessions(w http.ResponseWriter, r *http.Request, tenant TenantContext) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	if !canOperate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
		return
	}
	var request createChatSessionRequest
	if err := decodeStrict(r, &request); err != nil || !validResourceID(request.AppID) {
		writeError(w, http.StatusBadRequest, "invalid_chat_session", "app id is required")
		return
	}
	if _, exists := h.platform.app(tenant.TenantID, request.AppID); !exists {
		writeError(w, http.StatusNotFound, "agent_app_not_found", "Agent App was not found")
		return
	}
	sessionID := strings.TrimSpace(request.SessionID)
	if sessionID == "" {
		idBytes := make([]byte, 8)
		if _, err := rand.Read(idBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "session_id_unavailable", "session id could not be generated")
			return
		}
		sessionID = "chat-" + hex.EncodeToString(idBytes)
	}
	if !validResourceID(sessionID) {
		writeError(w, http.StatusBadRequest, "invalid_chat_session", "session id is invalid")
		return
	}
	store, release, err := h.acquireStore(tenant.TenantID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
		return
	}
	defer release()
	if _, err := store.GetSessionState(r.Context(), tenant.TenantID, sessionID); err == nil {
		writeError(w, http.StatusConflict, "chat_session_exists", "chat session already exists")
		return
	} else if !errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
		return
	}
	if err := h.appendChatEvent(r.Context(), store, tenant.TenantID, sessionID, "session-created", "session.created", map[string]string{
		"app_id": request.AppID, "user_id": tenant.UserID,
	}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "session event could not be persisted")
		return
	}
	if _, err := h.channels.CreateBinding(tenant, createChannelBindingRequest{
		Channel: ChannelMock, AppID: request.AppID, ConversationType: ConversationSingle,
		ConversationID: "chat:" + sessionID, UserID: tenant.UserID, SessionID: sessionID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "channel_binding_unavailable", "Mock IM binding could not be created")
		return
	}
	writeJSON(w, http.StatusCreated, Session{ID: sessionID, TenantID: tenant.TenantID, AppID: request.AppID, UserID: tenant.UserID})
}

func (h *AdminHandler) handleSendChatMessage(w http.ResponseWriter, r *http.Request, tenant TenantContext, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	if !canOperate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
		return
	}
	var request sendChatMessageRequest
	if err := decodeStrict(r, &request); err != nil || request.Input == "" {
		writeError(w, http.StatusBadRequest, "invalid_chat_message", "input is required")
		return
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = newRequestID()
	}
	if !validIdempotencyKey(requestID) {
		writeError(w, http.StatusBadRequest, "invalid_request_id", "request id must be 1 to 128 printable ASCII characters")
		return
	}
	events, err := h.chatEvents(r.Context(), tenant.TenantID, sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
		return
	}
	if len(events) == 0 {
		writeError(w, http.StatusNotFound, "chat_session_not_found", "chat session was not found")
		return
	}
	appID := chatSessionAppID(events)
	if appID == "" {
		writeError(w, http.StatusBadRequest, "chat_session_invalid", "chat session is invalid")
		return
	}
	result, err := h.startChatRun(chatRunOptions{
		tenant: tenant, appID: appID, sessionID: sessionID, input: request.Input,
		requestID: requestID, userID: tenant.UserID,
	})
	if err != nil {
		writeChatStartError(w, err)
		return
	}
	status := http.StatusAccepted
	if result.Status != "running" {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

func (h *AdminHandler) handleCancelChatRun(w http.ResponseWriter, r *http.Request, tenant TenantContext, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	if !canOperate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
		return
	}
	var request cancelChatRunRequest
	if err := decodeStrict(r, &request); err != nil || !validIdempotencyKey(request.RequestID) {
		writeError(w, http.StatusBadRequest, "invalid_chat_run", "request id is required")
		return
	}
	events, err := h.chatEvents(r.Context(), tenant.TenantID, sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
		return
	}
	if len(events) == 0 {
		writeError(w, http.StatusNotFound, "chat_session_not_found", "chat session was not found")
		return
	}
	runExists := false
	for _, event := range events {
		if event.IdempotencyKey == request.RequestID+":input" {
			runExists = true
			break
		}
	}
	if !runExists {
		writeError(w, http.StatusNotFound, "chat_run_not_found", "chat run was not found")
		return
	}
	key := chatRunKey(tenant.TenantID, sessionID, request.RequestID)
	h.chatMu.Lock()
	active, running := h.activeRuns[key]
	h.chatMu.Unlock()
	if running {
		active.cancel()
	}
	status := chatRunStatus(events, request.RequestID)
	if running {
		status = "running"
	} else if status == "running" {
		status = "pending"
	}
	writeJSON(w, http.StatusOK, chatRunResponse{SessionID: sessionID, RequestID: request.RequestID, Status: status})
}

func (h *AdminHandler) writeChatEvents(w http.ResponseWriter, r *http.Request, tenant TenantContext, sessionID string) {
	events, err := h.chatEvents(r.Context(), tenant.TenantID, sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
		return
	}
	if len(events) == 0 {
		writeError(w, http.StatusNotFound, "chat_session_not_found", "chat session was not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

type chatSSEEnvelope struct {
	EventID   string          `json:"event_id"`
	RequestID string          `json:"request_id"`
	SessionID string          `json:"session_id"`
	Sequence  uint64          `json:"sequence"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
}

func (h *AdminHandler) handleChatStream(w http.ResponseWriter, r *http.Request, tenant TenantContext, sessionID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
		return
	}
	store, release, err := h.acquireStore(tenant.TenantID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
		return
	}
	defer release()
	all, err := store.ListSessionEvents(r.Context(), tenant.TenantID, sessionID, 0)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_error", "storage unavailable")
		return
	}
	if len(all) == 0 {
		writeError(w, http.StatusNotFound, "chat_session_not_found", "chat session was not found")
		return
	}
	after := parseUintQuery(r.URL.Query().Get("after"))
	if after == 0 && r.Header.Get("Last-Event-ID") != "" {
		lastEventID := r.Header.Get("Last-Event-ID")
		for _, event := range all {
			if event.ID == lastEventID {
				after = event.Sequence
				break
			}
		}
	}
	requestID := r.URL.Query().Get("request_id")
	if requestID != "" && !validIdempotencyKey(requestID) {
		writeError(w, http.StatusBadRequest, "invalid_request_id", "request id must be 1 to 128 printable ASCII characters")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming is unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	terminal := false
	emit := func(events []SessionEvent) bool {
		for _, event := range events {
			envelope := chatSSEEnvelopeFor(event)
			if requestID != "" && envelope.RequestID != requestID {
				continue
			}
			if !writeSSEEvent(w, envelope) {
				return false
			}
			flusher.Flush()
			if isChatTerminalEvent(envelope) {
				terminal = true
				return false
			}
		}
		return true
	}
	initialEvents := make([]SessionEvent, 0, len(all))
	for _, event := range all {
		if event.Sequence > after {
			initialEvents = append(initialEvents, event)
		}
	}
	if !emit(initialEvents) {
		return
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !terminal {
		select {
		case <-r.Context().Done():
			return
		case <-h.chatCtx.Done():
			return
		case <-ticker.C:
			events, err := store.ListSessionEvents(r.Context(), tenant.TenantID, sessionID, after)
			if err != nil {
				return
			}
			if len(events) > 0 {
				after = events[len(events)-1].Sequence
			}
			if !emit(events) {
				return
			}
		}
	}
}

func parseUintQuery(value string) uint64 {
	var result uint64
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0
		}
		result = result*10 + uint64(char-'0')
	}
	return result
}

func chatSSEEnvelopeFor(event SessionEvent) chatSSEEnvelope {
	var payload struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(event.Payload, &payload)
	return chatSSEEnvelope{
		EventID: event.ID, RequestID: payload.RequestID, SessionID: event.SessionID,
		Sequence: event.Sequence, Type: event.Type, Data: append(json.RawMessage(nil), event.Payload...),
	}
}

func writeSSEEvent(w http.ResponseWriter, envelope chatSSEEnvelope) bool {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "id: %s\nretry: 1000\ndata: %s\n\n", envelope.EventID, encoded); err != nil {
		return false
	}
	return true
}

func isChatTerminalEvent(envelope chatSSEEnvelope) bool {
	switch envelope.Type {
	case "run.completed", "run.failed", "run.cancelled":
		return true
	default:
		return false
	}
}

type chatRunOptions struct {
	tenant    TenantContext
	appID     string
	sessionID string
	input     string
	requestID string
	userID    string
	binding   *ChannelBinding
}

type activeChatRun struct {
	cancel context.CancelFunc
	input  string
}

func (h *AdminHandler) startChatRun(options chatRunOptions) (chatRunResponse, error) {
	if !canOperate(options.tenant.Role) {
		return chatRunResponse{}, errors.New("forbidden")
	}
	store, release, err := h.acquireStore(options.tenant.TenantID)
	if err != nil {
		return chatRunResponse{}, errors.New("storage_error")
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()
	key := chatRunKey(options.tenant.TenantID, options.sessionID, options.requestID)
	if options.binding == nil {
		if binding, ok := h.channels.BindingForSession(options.tenant.TenantID, options.sessionID); ok {
			options.binding = &binding
		}
	}
	h.chatMu.Lock()
	if active, running := h.activeRuns[key]; running {
		if active.input != options.input {
			h.chatMu.Unlock()
			return chatRunResponse{}, errors.New("idempotency_key_reused")
		}
		h.chatMu.Unlock()
		return chatRunResponse{SessionID: options.sessionID, RequestID: options.requestID, Status: "running"}, nil
	}
	events, err := store.ListSessionEvents(h.chatCtx, options.tenant.TenantID, options.sessionID, 0)
	if err != nil {
		h.chatMu.Unlock()
		return chatRunResponse{}, errors.New("storage_error")
	}
	inputPayload, _ := json.Marshal(map[string]string{
		"app_id": options.appID, "input": options.input, "request_id": options.requestID, "user_id": options.userID,
	})
	recoveryOptions := options
	existingInput := false
	existingStarted := false
	for _, event := range events {
		if event.IdempotencyKey != options.requestID+":input" {
			continue
		}
		if event.Type != "message.input" || string(event.Payload) != string(inputPayload) {
			h.chatMu.Unlock()
			return chatRunResponse{}, errors.New("idempotency_key_reused")
		}
		if chatTerminalEvent(events, options.requestID) != nil {
			h.chatMu.Unlock()
			return chatRunResponse{SessionID: options.sessionID, RequestID: options.requestID, Status: chatRunStatus(events, options.requestID)}, nil
		}
		var persistedInput struct {
			AppID  string `json:"app_id"`
			Input  string `json:"input"`
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(event.Payload, &persistedInput) != nil || persistedInput.Input == "" {
			h.chatMu.Unlock()
			return chatRunResponse{}, errors.New("storage_error")
		}
		if persistedInput.AppID != "" {
			recoveryOptions.appID = persistedInput.AppID
		}
		if persistedInput.UserID != "" {
			recoveryOptions.userID = persistedInput.UserID
		}
		recoveryOptions.input = persistedInput.Input
		options = recoveryOptions
		existingInput = true
		break
	}
	for _, event := range events {
		if event.IdempotencyKey == options.requestID+":started" {
			existingStarted = true
			break
		}
	}
	if !existingInput {
		if err := store.AppendSessionEvent(h.chatCtx, SessionEvent{
			TenantID: options.tenant.TenantID, SessionID: options.sessionID, IdempotencyKey: options.requestID + ":input",
			Type: "message.input", Payload: inputPayload,
		}); err != nil {
			h.chatMu.Unlock()
			if errors.Is(err, ErrDuplicateEvent) {
				return chatRunResponse{}, errors.New("idempotency_key_reused")
			}
			return chatRunResponse{}, errors.New("storage_error")
		}
	}
	if !existingStarted {
		if err := h.appendChatEvent(h.chatCtx, store, options.tenant.TenantID, options.sessionID, options.requestID+":started", "run.started", map[string]string{
			"app_id": options.appID, "request_id": options.requestID,
		}); err != nil {
			h.chatMu.Unlock()
			return chatRunResponse{}, errors.New("storage_error")
		}
	}
	runCtx, cancel := context.WithCancel(h.chatCtx)
	h.activeRuns[key] = activeChatRun{cancel: cancel, input: options.input}
	h.chatWG.Add(1)
	started = true
	h.chatMu.Unlock()

	go func() {
		defer h.chatWG.Done()
		defer func() {
			h.chatMu.Lock()
			delete(h.activeRuns, key)
			h.chatMu.Unlock()
			cancel()
			release()
		}()
		h.runChat(runCtx, store, options)
	}()
	return chatRunResponse{SessionID: options.sessionID, RequestID: options.requestID, Status: "running"}, nil
}

func (h *AdminHandler) runChat(ctx context.Context, store DataStore, options chatRunOptions) {
	events, err := h.runtime.Stream(ctx, options.tenant, GatewayRequest{
		AppID: options.appID, SessionID: options.sessionID, Input: options.input, RequestID: options.requestID,
	})
	if err != nil {
		eventType := "run.failed"
		if err.Error() == "request_cancelled" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			eventType = "run.cancelled"
		}
		terminalCtx, cancel := context.WithTimeout(h.failureCtx, 2*time.Second)
		_ = h.appendCriticalChatEvent(terminalCtx, store, options.tenant.TenantID, options.sessionID, options.requestID+":terminal", eventType, h.chatIdentityPayload(options, map[string]string{"error": "run failed"}))
		cancel()
		return
	}
	var output string
	deltaNumber := 0
	for runtimeEvent := range events {
		if runtimeEvent.Type == "message.delta" {
			delta := runtimeEvent.Data["delta"]
			output += delta
			key := options.requestID + ":delta"
			if deltaNumber > 0 {
				key += fmt.Sprintf("-%d", deltaNumber)
			}
			deltaNumber++
			payload := h.chatIdentityPayload(options, runtimeEvent.Data)
			payload["delta"] = delta
			_ = h.appendChatEvent(ctx, store, options.tenant.TenantID, options.sessionID, key, runtimeEvent.Type, payload)
		}
		if runtimeEvent.Type == "message.completed" {
			if value := runtimeEvent.Data["output"]; value != "" {
				output = value
			}
			payload := h.chatIdentityPayload(options, runtimeEvent.Data)
			payload["output"] = output
			_ = h.appendChatEvent(ctx, store, options.tenant.TenantID, options.sessionID, options.requestID+":completed", runtimeEvent.Type, payload)
		}
		if runtimeEvent.Type == "run.failed" {
			terminalCtx, cancel := context.WithTimeout(h.failureCtx, 2*time.Second)
			payload := h.chatIdentityPayload(options, runtimeEvent.Data)
			payload["error"] = "run failed"
			_ = h.appendCriticalChatEvent(terminalCtx, store, options.tenant.TenantID, options.sessionID, options.requestID+":terminal", "run.failed", payload)
			cancel()
			return
		}
		if runtimeEvent.Type == "run.cancelled" {
			terminalCtx, cancel := context.WithTimeout(h.failureCtx, 2*time.Second)
			payload := h.chatIdentityPayload(options, runtimeEvent.Data)
			payload["error"] = "run cancelled"
			_ = h.appendCriticalChatEvent(terminalCtx, store, options.tenant.TenantID, options.sessionID, options.requestID+":terminal", "run.cancelled", payload)
			cancel()
			return
		}
	}
	if options.binding != nil {
		delivery, err := h.channels.Send(ctx, *options.binding, ChannelReply{MessageID: options.requestID, Text: output})
		if err == nil {
			_ = h.appendChatEvent(ctx, store, options.tenant.TenantID, options.sessionID, options.requestID+":reply", "channel.reply", h.chatIdentityPayload(options, map[string]string{
				"message_id": delivery.MessageID, "text": output, "status": delivery.Status,
			}))
		} else {
			deliveryCtx, cancel := context.WithTimeout(h.failureCtx, 2*time.Second)
			_ = h.appendChatEvent(deliveryCtx, store, options.tenant.TenantID, options.sessionID, options.requestID+":delivery", "channel.delivery", h.chatIdentityPayload(options, map[string]string{
				"message_id": options.requestID, "status": "failed", "code": channelErrorCode(err), "attempts": "bounded",
			}))
			cancel()
		}
	}
	if ctx.Err() != nil {
		terminalCtx, cancel := context.WithTimeout(h.failureCtx, 2*time.Second)
		_ = h.appendCriticalChatEvent(terminalCtx, store, options.tenant.TenantID, options.sessionID, options.requestID+":terminal", "run.cancelled", h.chatIdentityPayload(options, map[string]string{"error": "run cancelled"}))
		cancel()
		return
	}
	terminalCtx, cancelTerminal := context.WithTimeout(h.failureCtx, 2*time.Second)
	_ = h.appendCriticalChatEvent(terminalCtx, store, options.tenant.TenantID, options.sessionID, options.requestID+":run-completed", "run.completed", h.chatIdentityPayload(options, nil))
	cancelTerminal()
}

func (h *AdminHandler) chatIdentityPayload(options chatRunOptions, values map[string]string) map[string]string {
	payload := map[string]string{
		"tenant_id": options.tenant.TenantID, "app_id": options.appID, "session_id": options.sessionID,
		"user_id": options.userID, "request_id": options.requestID,
	}
	if deployment, ok := h.platform.activeDeployment(options.tenant.TenantID, options.appID); ok {
		payload["deployment_id"] = deployment.ID
		payload["version_id"] = deployment.VersionID
	}
	for key, value := range values {
		payload[key] = value
	}
	return payload
}

func (h *AdminHandler) appendChatEvent(ctx context.Context, store DataStore, tenantID, sessionID, idempotencyKey, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return store.AppendSessionEvent(ctx, SessionEvent{
		TenantID: tenantID, SessionID: sessionID, IdempotencyKey: idempotencyKey, Type: eventType, Payload: encoded,
	})
}

func (h *AdminHandler) appendCriticalChatEvent(ctx context.Context, store DataStore, tenantID, sessionID, idempotencyKey, eventType string, payload any) error {
	for {
		err := h.appendChatEvent(ctx, store, tenantID, sessionID, idempotencyKey, eventType, payload)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (h *AdminHandler) chatEvents(ctx context.Context, tenantID, sessionID string) ([]SessionEvent, error) {
	store, release, err := h.acquireStore(tenantID)
	if err != nil {
		return nil, err
	}
	defer release()
	return store.ListSessionEvents(ctx, tenantID, sessionID, 0)
}

func chatRunKey(tenantID, sessionID, requestID string) string {
	return tenantID + "\x00" + sessionID + "\x00" + requestID
}

func chatTerminalEvent(events []SessionEvent, requestID string) *SessionEvent {
	for index := range events {
		if events[index].IdempotencyKey == requestID+":terminal" || events[index].IdempotencyKey == requestID+":run-completed" {
			return &events[index]
		}
	}
	return nil
}

func chatRunStatus(events []SessionEvent, requestID string) string {
	terminal := chatTerminalEvent(events, requestID)
	if terminal == nil {
		return "running"
	}
	switch terminal.Type {
	case "run.cancelled":
		return "cancelled"
	case "run.failed":
		return "failed"
	default:
		return "completed"
	}
}

func chatSessionAppID(events []SessionEvent) string {
	for _, event := range events {
		if event.Type != "session.created" {
			continue
		}
		var payload struct {
			AppID string `json:"app_id"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.AppID != "" {
			return payload.AppID
		}
	}
	return ""
}

func newRequestID() string {
	idBytes := make([]byte, 12)
	_, _ = rand.Read(idBytes)
	return "request-" + hex.EncodeToString(idBytes)
}

func writeChatStartError(w http.ResponseWriter, err error) {
	switch err.Error() {
	case "forbidden":
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
	case "idempotency_key_reused":
		writeError(w, http.StatusConflict, "idempotency_key_reused", "request id was already used with different input")
	default:
		writeError(w, http.StatusServiceUnavailable, "storage_error", "session event could not be persisted")
	}
}

func writeChannelError(w http.ResponseWriter, err error) {
	code := channelErrorCode(err)
	status := http.StatusBadGateway
	switch code {
	case "channel_signature_invalid":
		status = http.StatusUnauthorized
	case "channel_callback_invalid", "channel_message_too_long", "channel_attachment_rejected":
		status = http.StatusBadRequest
	case "channel_rate_limited":
		status = http.StatusTooManyRequests
	case "channel_message_out_of_order":
		status = http.StatusConflict
	case "channel_timeout":
		status = http.StatusGatewayTimeout
	case "channel_disabled":
		status = http.StatusConflict
	}
	writeError(w, status, code, "channel delivery failed")
}
