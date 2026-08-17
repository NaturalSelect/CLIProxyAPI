package executor

import (
	"context"
	"testing"
)

func TestPreferUpstreamWebsocketContext(t *testing.T) {
	if PreferUpstreamWebsocket(nil) {
		t.Fatal("PreferUpstreamWebsocket(nil) = true, want false")
	}
	if PreferUpstreamWebsocket(context.Background()) {
		t.Fatal("PreferUpstreamWebsocket(background) = true, want false")
	}

	ctx := WithPreferUpstreamWebsocket(nil)
	if !PreferUpstreamWebsocket(ctx) {
		t.Fatal("PreferUpstreamWebsocket(marked context) = false, want true")
	}
	if DownstreamWebsocket(ctx) {
		t.Fatal("preferred upstream websocket context was also marked as downstream websocket")
	}
	if RequiredUpstreamWebsocket(ctx) {
		t.Fatal("preferred upstream websocket context was also marked as required upstream websocket")
	}
}
