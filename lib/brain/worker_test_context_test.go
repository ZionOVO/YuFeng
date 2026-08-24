package brain

import "context"

func workerCertContext(ctx context.Context, certificateSHA256 string) context.Context {
	return context.WithValue(ctx, clientCertHashKey{}, certificateSHA256)
}
