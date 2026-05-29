package keychainserver_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	apikeyv1 "github.com/elloloop/keychain/gen/apikey/v1"
	"github.com/elloloop/keychain/keychainserver"
	"github.com/elloloop/keychain/keychainserver/store/memory"
)

func TestGRPCServiceCriticalPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	svc, err := keychainserver.New(ctx, keychainserver.Options{Store: memory.New()})
	if err != nil {
		t.Fatalf("keychainserver.New: %v", err)
	}

	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	apikeyv1.RegisterApiKeyServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := apikeyv1.NewApiKeyServiceClient(conn)

	ws, err := client.CreateWorkspace(ctx, &apikeyv1.CreateWorkspaceRequest{
		Name:             "e2e",
		OwnerPrincipalId: "owner",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	api, err := client.CreateApi(ctx, &apikeyv1.CreateApiRequest{
		WorkspaceId: ws.GetWorkspace().GetWorkspaceId(),
		Name:        "prod",
		KeyPrefix:   "ck_grpc_",
	})
	if err != nil {
		t.Fatalf("CreateApi: %v", err)
	}
	key, err := client.CreateKey(ctx, &apikeyv1.CreateKeyRequest{
		ApiId:            api.GetApi().GetApiId(),
		OwnerPrincipalId: "user_1",
		Name:             "grpc-key",
		Permissions:      []string{"chat:write"},
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if key.GetPlaintext() == "" {
		t.Fatal("CreateKey returned empty plaintext")
	}

	valid, err := client.VerifyKey(ctx, &apikeyv1.VerifyKeyRequest{
		Plaintext:           key.GetPlaintext(),
		RequiredPermissions: []string{"chat:write"},
	})
	if err != nil {
		t.Fatalf("VerifyKey valid: %v", err)
	}
	if valid.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_VALID {
		t.Fatalf("valid result = %v, want VALID", valid.GetResult())
	}

	rotated, err := client.RotateKey(ctx, &apikeyv1.RotateKeyRequest{KeyId: key.GetKey().GetKeyId()})
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	oldVerify, err := client.VerifyKey(ctx, &apikeyv1.VerifyKeyRequest{Plaintext: key.GetPlaintext()})
	if err != nil {
		t.Fatalf("VerifyKey old plaintext: %v", err)
	}
	if oldVerify.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_NOT_FOUND {
		t.Fatalf("old plaintext result = %v, want NOT_FOUND", oldVerify.GetResult())
	}
	newVerify, err := client.VerifyKey(ctx, &apikeyv1.VerifyKeyRequest{Plaintext: rotated.GetPlaintext()})
	if err != nil {
		t.Fatalf("VerifyKey new plaintext: %v", err)
	}
	if newVerify.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_VALID {
		t.Fatalf("new plaintext result = %v, want VALID", newVerify.GetResult())
	}

	if _, err := client.RevokeKey(ctx, &apikeyv1.RevokeKeyRequest{KeyId: key.GetKey().GetKeyId()}); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	revoked, err := client.VerifyKey(ctx, &apikeyv1.VerifyKeyRequest{Plaintext: rotated.GetPlaintext()})
	if err != nil {
		t.Fatalf("VerifyKey revoked: %v", err)
	}
	if revoked.GetResult() != apikeyv1.VerifyResult_VERIFY_RESULT_REVOKED {
		t.Fatalf("revoked result = %v, want REVOKED", revoked.GetResult())
	}
}
