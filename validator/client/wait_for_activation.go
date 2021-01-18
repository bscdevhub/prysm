package client

import (
	"context"
	"fmt"
	"github.com/prysmaticlabs/prysm/shared/params"
	"github.com/prysmaticlabs/prysm/shared/slotutil"
	"io"
	"time"

	"github.com/pkg/errors"
	ethpb "github.com/prysmaticlabs/ethereumapis/eth/v1alpha1"
	"github.com/prysmaticlabs/prysm/shared/bytesutil"
	"github.com/prysmaticlabs/prysm/shared/mathutil"
	"github.com/prysmaticlabs/prysm/shared/traceutil"
	"github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
)

// WaitForActivation checks whether the validator pubkey is in the active
// validator set. If not, this operation will block until an activation message is
// received.
func (v *validator) WaitForActivation(ctx context.Context) error {
	ctx, span := trace.StartSpan(ctx, "validator.WaitForActivation")
	defer span.End()

	validatingKeys, err := v.keyManager.FetchValidatingPublicKeys(ctx)
	if err != nil {
		return errors.Wrap(err, "could not fetch validating keys")
	}
	if len(validatingKeys) == 0 {
		log.Warn(msgNoKeysFetched)

		ticker := time.NewTicker(keyRefetchPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				keys, err := v.keyManager.FetchValidatingPublicKeys(ctx)
				if err != nil {
					return errors.Wrap(err, msgCouldNotFetchKeys)
				}
				if len(keys) == 0 {
					log.Warn(msgNoKeysFetched)
					continue
				}
				// after this statement we jump out of `select` and hit `break`,
				// thus jumping out of `for` into the rest of the function
				validatingKeys = keys
			case <-ctx.Done():
				log.Debug("Context closed, exiting fetching validating keys")
				return ctx.Err()
			}
			break
		}
	}

	req := &ethpb.ValidatorActivationRequest{
		PublicKeys: bytesutil.FromBytes48Array(validatingKeys),
	}
	stream, err := v.validatorClient.WaitForActivation(ctx, req)
	if err != nil {
		traceutil.AnnotateError(span, err)
		attempts := getStreamAttempts(ctx)
		log.WithError(err).WithField("attempts", attempts).
			Error("Stream broken while waiting for activation. Reconnecting...")
		// Reconnection attempt backoff, up to 60s.
		time.Sleep(time.Second * time.Duration(mathutil.Min(uint64(attempts), 60)))
		return v.WaitForActivation(incrementRetries(ctx))
	}
	for {
		res, err := stream.Recv()
		// If the stream is closed, we stop the loop.
		if errors.Is(err, io.EOF) {
			break
		}
		// If context is canceled we stop the loop.
		if ctx.Err() == context.Canceled {
			return errors.Wrap(ctx.Err(), "context has been canceled so shutting down the loop")
		}
		if err != nil {
			traceutil.AnnotateError(span, err)
			attempts := getStreamAttempts(ctx)
			log.WithError(err).WithField("attempts", attempts).
				Error("Stream broken while waiting for activation. Reconnecting...")
			// Reconnection attempt backoff, up to 60s.
			time.Sleep(time.Second * time.Duration(mathutil.Min(uint64(attempts), 60)))
			return v.WaitForActivation(incrementRetries(ctx))
		}
		valActivated := v.checkAndLogValidatorStatus(res.Statuses)

		if valActivated {
			for _, statusResp := range res.Statuses {
				if statusResp.Status.Status != ethpb.ValidatorStatus_ACTIVE {
					continue
				}
				log.WithFields(logrus.Fields{
					"publicKey": fmt.Sprintf("%#x", bytesutil.Trunc(statusResp.PublicKey)),
					"index":     statusResp.Index,
				}).Info("Validator activated")
			}
			break
		}
	}
	v.ticker = slotutil.GetSlotTicker(time.Unix(int64(v.genesisTime), 0), params.BeaconConfig().SecondsPerSlot)
	return nil
}

// Preferred way to use context keys is with a non built-in type. See: RVV-B0003
type waitForActivationContextKey string

const waitForActivationAttemptsContextKey = waitForActivationContextKey("WaitForActivation-attempts")

func getStreamAttempts(ctx context.Context) int {
	attempts, ok := ctx.Value(waitForActivationAttemptsContextKey).(int)
	if !ok {
		return 1
	}
	return attempts
}

func incrementRetries(ctx context.Context) context.Context {
	attempts := getStreamAttempts(ctx)
	return context.WithValue(ctx, waitForActivationAttemptsContextKey, attempts+1)
}
