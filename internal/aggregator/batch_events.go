// Copyright © 2021 Kaleido, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aggregator

import (
	"context"
	"encoding/json"
	"io"

	"github.com/gofrs/uuid"
	"github.com/kaleido-io/firefly/internal/blockchain"
	"github.com/kaleido-io/firefly/internal/database"
	"github.com/kaleido-io/firefly/internal/fftypes"
	"github.com/kaleido-io/firefly/internal/log"
)

// SequencedBroadcastBatch is called in-line with a particular ledger's stream of events, so while we
// block here this blockchain event remains un-acknowledged, and no further events will arrive from this
// particular ledger.
//
// We must block here long enough to get the payload from the publicstorage, persist the messages in the correct
// sequence, and also persist all the data.
func (a *aggregator) SequencedBroadcastBatch(batch *blockchain.BroadcastBatch, author string, protocolTxId string, additionalInfo map[string]interface{}) error {

	var batchID uuid.UUID
	copy(batchID[:], batch.BatchID[0:16])

	var body io.ReadCloser
	if err := a.retry.Do(a.ctx, func(attempt int) (retry bool, err error) {
		body, err = a.publicstorage.RetrieveData(a.ctx, batch.BatchPaylodRef)
		return err != nil, err // retry indefinitely (until context closes)
	}); err != nil {
		return err
	}
	defer body.Close()

	var batchData *fftypes.Batch
	err := json.NewDecoder(body).Decode(&batchData)
	if err != nil {
		log.L(a.ctx).Errorf("Failed to parse payload referred in batch ID '%s' from transaction '%s'", batchID, protocolTxId)
		return nil // log and swallow unprocessable data
	}

	// At this point the batch is parsed, so any errors in processing need to be considered as:
	// 1) Retryable - any transient error returned by processBatch is retried indefinitely
	// 2) Swallowable - the data is invalid, and we have to move onto subsequent messages
	// 3) Server shutting down - the context is cancelled (handled by retry)
	return a.retry.Do(a.ctx, func(attempt int) (bool, error) {
		// We process the batch into the DB as a single transaction (if transactions are supported), both for
		// efficiency and to minimize the chance of duplicates (although at-least-once delivery is the core model)
		err := a.database.RunAsGroup(a.ctx, func(ctx context.Context) error {
			return a.persistBatch(ctx, batchData, author, protocolTxId, additionalInfo)
		})
		return err != nil, err // retry indefinitely (until context closes)
	})
}

// persistBatch performs very simple validation on each message/data element (hashes) and either persists
// or discards them. Errors are returned only in the case of database failures, which should be retried.
func (a *aggregator) persistBatch(ctx context.Context /* db TX context*/, batch *fftypes.Batch, author string, protocolTxId string, additionalInfo map[string]interface{}) error {
	l := log.L(ctx)
	now := fftypes.Now()

	if batch.ID == nil || batch.Payload.TX.ID == nil {
		l.Errorf("Invalid batch '%s'. Missing ID (%v) or payload ID (%v)", batch.ID, batch.ID, batch.Payload.TX.ID)
		return nil // This is not retryable. skip this batch
	}

	// Verify the hash calculation
	hash := batch.Payload.Hash()
	if batch.Hash == nil || *batch.Hash != *hash {
		l.Errorf("Invalid batch '%s'. Hash does not match payload. Found=%s Expected=%s", batch.ID, hash, batch.Hash)
		return nil // This is not retryable. skip this batch
	}

	// Verify the author matches
	if batch.Author != author {
		l.Errorf("Invalid batch '%s'. Author '%s' does not match transaction submitter '%s", batch.ID, batch.Author, author)
		return nil // This is not retryable. skip this batch
	}

	// Set confirmed on the batch
	batch.Confirmed = now

	// Upsert the batch itself, ensuring the hash does not change
	err := a.database.UpsertBatch(ctx, batch, false)
	if err != nil {
		if err == database.HashMismatch {
			l.Errorf("Invalid batch '%s'. Batch hash mismatch with existing record", batch.ID)
			return nil // This is not retryable. skip this batch
		}
		l.Errorf("Failed to insert batch '%s': %s", batch.ID, err)
		return err // a peristence failure here is considered retryable (so returned)
	}

	// Get any existing record for the batch transaction record
	tx, _ := a.database.GetTransactionById(ctx, batch.Namespace, batch.Payload.TX.ID)
	if tx == nil {
		// We're the first to write the transaction record on this node
		tx = &fftypes.Transaction{
			ID: batch.Payload.TX.ID,
			Subject: fftypes.TransactionSubject{
				Type:      fftypes.TransactionTypePin,
				Author:    author,
				Namespace: batch.Namespace,
				Batch:     batch.ID,
			},
		}
		tx.Hash = tx.Subject.Hash()
	} else if tx.Subject.Type != fftypes.TransactionTypePin ||
		tx.Subject.Author != author ||
		tx.Subject.Namespace != batch.Namespace ||
		tx.Subject.Batch == nil ||
		*tx.Subject.Batch != *batch.ID {
		l.Errorf("Invalid batch '%s'. Existing transaction '%s' does not match batch subject", batch.ID, tx.ID)
		return nil // This is not retryable. skip this batch
	}

	// Set the updates on the transaction
	tx.Confirmed = now
	tx.ProtocolID = protocolTxId
	tx.Info = additionalInfo
	tx.Status = fftypes.TransactionStatusConfirmed

	// Upsert the transaction, ensuring the hash does not change
	err = a.database.UpsertTransaction(ctx, tx, false)
	if err != nil {
		if err == database.HashMismatch {
			l.Errorf("Invalid batch '%s'. Transaction '%s' hash mismatch with existing record", batch.ID, tx.Hash)
			return nil // This is not retryable. skip this batch
		}
		l.Errorf("Failed to insert transaction for batch '%s': %s", batch.ID, err)
		return err // a peristence failure here is considered retryable (so returned)
	}

	// Insert the data entries
	for i, data := range batch.Payload.Data {
		if err = a.persistBatchData(ctx, batch, i, data); err != nil {
			return err
		}
	}

	// Insert the message entries
	for i, msg := range batch.Payload.Messages {
		if err = a.persistBatchMessage(ctx, batch, now, i, msg); err != nil {
			return err
		}
	}

	return nil

}

func (a *aggregator) persistBatchData(ctx context.Context /* db TX context*/, batch *fftypes.Batch, i int, data *fftypes.Data) error {
	l := log.L(ctx)
	l.Tracef("Batch %s data %d: %+v", batch.ID, i, data)

	if data == nil {
		l.Errorf("null data entry %d in batch '%s'", i, batch.ID)
		return nil // skip data entry
	}

	hash, err := data.Value.Hash(ctx, "value")
	if err != nil || data.Hash == nil || *data.Hash != *hash {
		l.Errorf("Invalid data entry %d in batch '%s'. Hash does not match value. Found=%s Expected=%s (err=%s)", i, batch.ID, hash, data.Hash, err)
		return nil // skip data entry
	}

	// Insert the data, ensuring the hash doesn't change
	if err = a.database.UpsertData(ctx, data, false); err != nil {
		if err == database.HashMismatch {
			l.Errorf("Invalid data entry %d in batch '%s'. Hash mismatch with existing record with same UUID '%s' Hash=%s", i, batch.ID, data.ID, data.Hash)
			return nil // This is not retryable. skip this data entry
		}
		l.Errorf("Failed to insert data entry %d in batch '%s': %s", i, batch.ID, err)
		return err // a peristence failure here is considered retryable (so returned)
	}

	return nil
}

func (a *aggregator) persistBatchMessage(ctx context.Context /* db TX context*/, batch *fftypes.Batch, now *fftypes.FFTime, i int, msg *fftypes.Message) error {
	l := log.L(ctx)
	l.Tracef("Batch %s message %d: %+v", batch.ID, i, msg)

	if msg == nil {
		l.Errorf("null message entry %d in batch '%s'", i, batch.ID)
		return nil // skip data entry
	}

	err := msg.Verify(ctx)
	if err != nil {
		l.Errorf("Invalid message entry %d in batch '%s': %s", i, batch.ID, err)
		return nil // skip message entry
	}

	// Set the confirmed timestamp on the message
	msg.Confirmed = now
	msg.BatchID = batch.ID

	// Insert the message, ensuring the hash doesn't change
	if err = a.database.UpsertMessage(ctx, msg, false); err != nil {
		if err == database.HashMismatch {
			l.Errorf("Invalid message entry %d in batch '%s'. Hash mismatch with existing record with same UUID '%s' Hash=%s", i, batch.ID, msg.Header.ID, msg.Hash)
			return nil // This is not retryable. skip this data entry
		}
		l.Errorf("Failed to insert message entry %d in batch '%s': %s", i, batch.ID, err)
		return err // a peristence failure here is considered retryable (so returned)
	}

	return nil
}