package kv

import (
	"context"

	bolt "go.etcd.io/bbolt"

	"github.com/prysmaticlabs/prysm/shared/bytesutil"
	"github.com/prysmaticlabs/prysm/shared/params"
)

// PruneAttestationsOlderThanCurrentWeakSubjectivity loops through every
// public key in the public keys bucket and prunes all attestation data
// that has target epochs older than the highest weak subjectivity period
// in our database. This routine is meant to run on startup.
func (s *Store) PruneAttestationsOlderThanCurrentWeakSubjectivity(ctx context.Context) error {
	return s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(pubKeysBucket)
		return bucket.ForEach(func(pubKey []byte, _ []byte) error {
			pkBucket := bucket.Bucket(pubKey)
			if pkBucket == nil {
				return nil
			}
			if err := pruneSourceEpochsBucket(pkBucket); err != nil {
				return err
			}
			return pruneSigningRootsBucket(pkBucket)
		})
	})
}

func pruneSourceEpochsBucket(bucket *bolt.Bucket) error {
	wssPeriod := params.BeaconConfig().WeakSubjectivityPeriod
	sourceEpochsBucket := bucket.Bucket(attestationSourceEpochsBucket)
	if sourceEpochsBucket == nil {
		return nil
	}
	// We obtain the highest source epoch from the source epochs bucket.
	// Then, we obtain the corresponding target epoch for that source epoch.
	highestSourceEpochBytes, _ := sourceEpochsBucket.Cursor().Last()
	highestTargetEpochBytes := sourceEpochsBucket.Get(highestSourceEpochBytes)
	highestTargetEpoch := bytesutil.BytesToUint64BigEndian(highestTargetEpochBytes)

	// No need to prune if the highest epoch we've written is still
	// before the first weak subjectivity period.
	if highestTargetEpoch < wssPeriod {
		return nil
	}

	return sourceEpochsBucket.ForEach(func(k []byte, v []byte) error {
		targetEpoch := bytesutil.BytesToUint64BigEndian(v)

		// For each source epoch we find, we check
		// if its associated target epoch is less than the weak
		// subjectivity period of the highest written target epoch
		// in the bucket and delete if so.
		if olderThanCurrentWeakSubjectivityPeriod(targetEpoch, highestTargetEpoch) {
			return sourceEpochsBucket.Delete(k)
		}
		return nil
	})
}

func pruneSigningRootsBucket(bucket *bolt.Bucket) error {
	wssPeriod := params.BeaconConfig().WeakSubjectivityPeriod
	signingRootsBucket := bucket.Bucket(attestationSigningRootsBucket)
	if signingRootsBucket == nil {
		return nil
	}

	// We obtain the highest target epoch from the signing roots bucket.
	highestTargetEpochBytes, _ := signingRootsBucket.Cursor().Last()
	highestTargetEpoch := bytesutil.BytesToUint64BigEndian(highestTargetEpochBytes)

	// No need to prune if the highest epoch we've written is still
	// before the first weak subjectivity period.
	if highestTargetEpoch < wssPeriod {
		return nil
	}

	return signingRootsBucket.ForEach(func(k []byte, v []byte) error {
		targetEpoch := bytesutil.BytesToUint64BigEndian(k)
		// For each target epoch we find in the bucket, we check
		// if it less than the weak subjectivity period of the
		// highest written target epoch in the bucket and delete if so.
		if olderThanCurrentWeakSubjectivityPeriod(targetEpoch, highestTargetEpoch) {
			return signingRootsBucket.Delete(k)
		}
		return nil
	})
}

func olderThanCurrentWeakSubjectivityPeriod(epoch, highestEpoch uint64) bool {
	wssPeriod := params.BeaconConfig().WeakSubjectivityPeriod
	// Number of weak subjectivity periods that have passed.
	currentWeakSubjectivityPeriod := highestEpoch / wssPeriod
	// We check if either the epoch is less than WEAK_SUBJECTIVITY_PERIOD
	// or is it is from a weak subjectivity period older than the current one,
	// for example, if 5 weak subjectivity periods have passed and epoch is
	// from 2 weak subjectivity periods ago, then we return true.
	return epoch < wssPeriod || (epoch/wssPeriod) < currentWeakSubjectivityPeriod
}
