//go:build integration

package protobuf_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	cfg "github.com/tsarna/vinculum/config"
	"go.uber.org/zap"

	// Register the wire:: functions and the protobuf wire format so the config
	// pipeline resolves them, exactly as the real binary does via cmd/plugins.go.
	_ "github.com/tsarna/vinculum/functions"
	_ "github.com/tsarna/vinculum/wireformats/protobuf"
)

// TestIntegration_ProtobufRoundTrip compiles testdata/orders.proto with a real
// protoc, then drives the full config pipeline over a VCL config that
// round-trips a value through wire::serialize / wire::deserialize against the
// protobuf wire format. The assertions in the VCL evaluate at config-processing
// time, so a broken round-trip surfaces as a diagnostic error.
//
// Run with: go test -tags integration ./wireformats/protobuf/
// Skips when protoc is not installed.
func TestIntegration_ProtobufRoundTrip(t *testing.T) {
	binpb := compileDescriptorSet(t)

	t.Run("round trip", func(t *testing.T) {
		_, diags := cfg.NewConfig().
			WithLogger(zap.NewNop()).
			WithFeature("readfiles", filepath.Dir(binpb)).
			WithSources("testdata/orders_roundtrip.vcl").
			Build()
		require.False(t, diags.HasErrors(), "round-trip config should be valid: %s", diags)
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		_, diags := cfg.NewConfig().
			WithLogger(zap.NewNop()).
			WithFeature("readfiles", filepath.Dir(binpb)).
			WithSources("testdata/orders_unknown_field.vcl").
			Build()
		require.True(t, diags.HasErrors(), "serializing an unknown field must error")
		require.Contains(t, diags.Error(), "unknown field")
	})
}

// compileDescriptorSet runs protoc over testdata/orders.proto, writing a
// FileDescriptorSet named orders.binpb into a temp dir, and returns its path.
// The temp dir is used as the config --file-path so descriptor_set =
// "orders.binpb" resolves to it.
func compileDescriptorSet(t *testing.T) string {
	t.Helper()

	protoc, err := exec.LookPath("protoc")
	if err != nil {
		t.Skip("protoc not installed; skipping protobuf integration test")
	}

	testdata, err := filepath.Abs("testdata")
	require.NoError(t, err)

	tmp := t.TempDir()
	binpb := filepath.Join(tmp, "orders.binpb")

	cmd := exec.Command(protoc,
		"--include_imports",
		"--descriptor_set_out="+binpb,
		"-I", testdata,
		"orders.proto",
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("protoc failed: %v", err)
	}
	require.FileExists(t, binpb)
	return binpb
}
