package brain

import (
	"context"

	"connectrpc.com/connect"

	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
)

func invokeDual(ctx context.Context, gateway *ToolGatewayServer, accessToken, capabilityToken, toolName, arguments string) (*connect.Response[toolgatewayv1.InvokeToolResponse], error) {
	request := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: toolName, ArgsJson: arguments})
	request.Header().Set("Authorization", "Bearer "+accessToken)
	request.Header().Set(CapabilityHeader, "Bearer "+capabilityToken)
	return gateway.InvokeTool(ctx, request)
}
