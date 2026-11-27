package main

import (
	"context"
	"errors"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/collisionc/silo-plugin-metadata-youtube/provider"
	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

// statusError gives an error the gRPC status code Silo decides by. Silo
// retries an unmatched folder soon after a transient failure (Unavailable,
// ResourceExhausted, DeadlineExceeded) and backs off up to a day, but after
// three failures it takes as permanent it parks the folder until its files
// or the plugin's settings change. A spent daily quota, an outage or a
// listing still running must not park a folder; a missing or rejected key
// rightly waits for the settings to change. The message is the error's own,
// which keeps "HTTP 404" in it for Silo's stale-ID check.
func statusError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(statusCode(err), err.Error())
}

func statusCode(err error) codes.Code {
	var apiErr *youtube.APIError
	switch {
	case errors.Is(err, youtube.ErrNoAPIKey):
		return codes.FailedPrecondition
	case errors.Is(err, provider.ErrStillWorking), errors.Is(err, youtube.ErrRequestFailed):
		return codes.Unavailable
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.As(err, &apiErr):
		switch {
		case apiErr.QuotaExceeded(), apiErr.RateLimited():
			return codes.ResourceExhausted
		case apiErr.KeyRejected():
			return codes.FailedPrecondition
		case apiErr.Status >= http.StatusInternalServerError:
			return codes.Unavailable
		case apiErr.Status == http.StatusNotFound:
			return codes.NotFound
		case apiErr.Status == http.StatusForbidden:
			return codes.PermissionDenied
		case apiErr.Status == http.StatusBadRequest:
			return codes.InvalidArgument
		}
	}
	return codes.Unknown
}
