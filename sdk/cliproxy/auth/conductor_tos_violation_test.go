package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type tosTestError struct {
	meta map[string]string
}

func (e tosTestError) Error() string                        { return "TOS_VIOLATION" }
func (e tosTestError) StatusCode() int                      { return http.StatusForbidden }
func (e tosTestError) IsTerminalTOSViolation() bool         { return true }
func (e tosTestError) IsCredentialScoped() bool             { return true }
func (e tosTestError) TOSAppealMetadata() map[string]string { return e.meta }

func registerTOSTestAuth(t *testing.T, id string) (*Manager, *Auth) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: id, Provider: "antigravity", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	return manager, auth
}

func TestResultErrorFromErrorCarriesTOSViolation(t *testing.T) {
	meta := map[string]string{"tos_appeal_url": "https://forms.gle/x", "tos_appeal_url_text": "Submit Appeal"}
	resultErr := resultErrorFromError(tosTestError{meta: meta})
	if resultErr == nil || !isTOSViolationResultError(resultErr) {
		t.Fatalf("resultErrorFromError() = %+v, want tos_violation code", resultErr)
	}
	if resultErr.HTTPStatus != http.StatusForbidden {
		t.Fatalf("HTTPStatus = %d, want 403", resultErr.HTTPStatus)
	}
	if resultErr.Metadata["tos_appeal_url"] != "https://forms.gle/x" {
		t.Fatalf("Metadata = %v, want appeal url preserved", resultErr.Metadata)
	}

	if plain := resultErrorFromError(errors.New("permission denied")); isTOSViolationResultError(plain) {
		t.Fatal("plain error must not be classified as a TOS violation")
	}
}

func TestErrorCloneCopiesMetadata(t *testing.T) {
	original := &Error{Code: "tos_violation", Metadata: map[string]string{"k": "v"}}
	clone := original.Clone()
	clone.Metadata["k"] = "changed"
	if original.Metadata["k"] != "v" {
		t.Fatal("Clone() shares the Metadata map with the original")
	}
	if (*Error)(nil).Clone() != nil {
		t.Fatal("nil Clone() must return nil")
	}
}

func TestMarkResultTOSViolationDisablesCredential(t *testing.T) {
	manager, auth := registerTOSTestAuth(t, "tos-cred")

	manager.MarkResult(context.Background(), Result{
		AuthID:          auth.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-pro",
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			HTTPStatus: http.StatusForbidden,
			Code:       tosViolationErrorCode,
			Message:    "TOS_VIOLATION",
			Metadata:   map[string]string{"tos_appeal_url": "https://forms.gle/hGzM9MEUv2azZsrb9", "tos_appeal_url_text": "Submit Appeal"},
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found after MarkResult")
	}
	if updated.Status != StatusDisabled || !updated.Disabled || !updated.Unavailable {
		t.Fatalf("status=%q disabled=%v unavailable=%v, want a disabled and unavailable credential", updated.Status, updated.Disabled, updated.Unavailable)
	}
	if updated.StatusMessage != tosViolationErrorCode {
		t.Fatalf("StatusMessage = %q, want %q", updated.StatusMessage, tosViolationErrorCode)
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero for a terminal state", updated.NextRetryAfter)
	}
	if appeal, _ := updated.Metadata["tos_appeal_url"].(string); appeal != "https://forms.gle/hGzM9MEUv2azZsrb9" {
		t.Fatalf("Metadata[tos_appeal_url] = %q, want appeal url preserved", appeal)
	}
	if appealText, _ := updated.Metadata["tos_appeal_url_text"].(string); appealText != "Submit Appeal" {
		t.Fatalf("Metadata[tos_appeal_url_text] = %q, want appeal text preserved", appealText)
	}
}

func TestMarkResultTOSViolationWithoutModelDisablesCredential(t *testing.T) {
	manager, auth := registerTOSTestAuth(t, "tos-no-model")

	manager.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: tosViolationErrorCode},
	})

	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusDisabled || !updated.Disabled {
		t.Fatalf("status=%q disabled=%v, want disabled", updated.Status, updated.Disabled)
	}
}

func TestMarkResultNonTOS403StillCools(t *testing.T) {
	manager, auth := registerTOSTestAuth(t, "plain-403")

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "antigravity",
		Model:    "gemini-3-pro",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "permission denied"},
	})

	updated, _ := manager.GetByID(auth.ID)
	if updated.Status == StatusDisabled || updated.Disabled {
		t.Fatal("plain 403 must not deactivate the credential")
	}
	if updated.ModelStates["gemini-3-pro"] == nil || updated.ModelStates["gemini-3-pro"].NextRetryAfter.IsZero() {
		t.Fatal("plain 403 must keep the model cooldown")
	}
}

func TestMarkResultTOSViolationLateResultsDoNotRevive(t *testing.T) {
	manager, auth := registerTOSTestAuth(t, "tos-late")
	ctx := context.Background()

	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Model: "gemini-3-pro",
		Success: false, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: tosViolationErrorCode},
	})
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Model: "gemini-3-pro", Success: true,
	})
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Model: "gemini-3-pro", Success: false,
		Error: &Error{HTTPStatus: http.StatusForbidden, Message: "plain 403"},
	})
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusDisabled || !updated.Disabled {
		t.Fatalf("late result revived the credential: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
	if updated.StatusMessage != tosViolationErrorCode {
		t.Fatalf("StatusMessage = %q, want %q preserved", updated.StatusMessage, tosViolationErrorCode)
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero after late failures", updated.NextRetryAfter)
	}
}
