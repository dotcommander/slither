package slither

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type phaseFOutcomeWriter struct {
	parent, name string
	parentInfo   fs.FileInfo
	targetInfo   fs.FileInfo
}

func outcomeWriterForPath(path string) (outcomeWriter, error) {
	if path == "" {
		return nil, errors.New("outcome path is empty")
	}
	clean := filepath.Clean(path)
	if filepath.Base(clean) == "." {
		return nil, errors.New("outcome path has no file name")
	}
	writer := phaseFOutcomeWriter{parent: filepath.Dir(clean), name: filepath.Base(clean)}
	if err := writer.bind(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (writer *phaseFOutcomeWriter) bind() error {
	root, err := outcomeRootOpener(writer.parent)
	if err != nil {
		return fmt.Errorf("open outcome parent: %w", err)
	}
	defer root.Close()
	parent, err := root.Stat(".")
	if err != nil || !parent.IsDir() {
		return errors.New("outcome parent is not a directory")
	}
	target, err := root.Lstat(writer.name)
	if errors.Is(err, fs.ErrNotExist) {
		file, openErr := root.OpenFile(writer.name, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			if errors.Is(openErr, fs.ErrExist) {
				return errors.New("outcome ledger was created concurrently")
			}
			return fmt.Errorf("create outcome ledger: %w", openErr)
		}
		target, err = verifyOutcomeFile(root, writer.name, file, nil)
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("lstat outcome ledger: %w", err)
	} else if err := validateOutcomeTarget(target); err != nil {
		return err
	}
	writer.parentInfo, writer.targetInfo = parent, target
	return nil
}

func (writer phaseFOutcomeWriter) WriteOutcome(ctx context.Context, report Report, feedback outcomeFeedback) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := deriveOutcomeRecord(report, feedback.ReportID, feedback.EvidenceID, feedback.Verdict, feedback.FilesOpened, feedback.ToolCalls, feedback.ReviewMS)
	if err != nil {
		return err
	}
	if err := validateStoredOutcomeRecord(record); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode outcome record: %w", err)
	}
	if len(encoded)+1 > maxOutcomeRecordLen {
		return errors.New("outcome record exceeds 1 MiB")
	}
	return writer.append(ctx, append(encoded, '\n'))
}

func (writer phaseFOutcomeWriter) append(ctx context.Context, record []byte) error {
	root, err := outcomeRootOpener(writer.parent)
	if err != nil {
		return fmt.Errorf("open outcome parent: %w", err)
	}
	defer root.Close()
	parent, err := root.Stat(".")
	if err != nil || !os.SameFile(parent, writer.parentInfo) {
		return errors.New("outcome parent changed after initialization")
	}
	pre, err := root.Lstat(writer.name)
	if err != nil || !os.SameFile(pre, writer.targetInfo) {
		return errors.New("outcome ledger changed after initialization")
	}
	if err := validateOutcomeTarget(pre); err != nil {
		return err
	}
	file, err := root.OpenFile(writer.name, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open outcome ledger: %w", err)
	}
	post, verifyErr := verifyOutcomeFile(root, writer.name, file, writer.targetInfo)
	if verifyErr != nil {
		_ = file.Close()
		return verifyErr
	}
	if !os.SameFile(post, pre) {
		_ = file.Close()
		return errors.New("outcome ledger changed while opening")
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return err
	}
	n, writeErr := file.Write(record)
	if writeErr != nil || n != len(record) {
		_ = file.Close()
		if writeErr != nil {
			return fmt.Errorf("append outcome ledger: %w", writeErr)
		}
		return errors.New("append outcome ledger: short write")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync outcome ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close outcome ledger: %w", err)
	}
	return nil
}

func validateOutcomeTarget(info fs.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o600 != 0o600 {
		return errors.New("outcome ledger must be an owner-readable, owner-writable regular file")
	}
	return nil
}

func verifyOutcomeFile(root outcomeRoot, name string, file outcomeFile, bound fs.FileInfo) (fs.FileInfo, error) {
	descriptor, err := file.Stat()
	if err != nil || !descriptor.Mode().IsRegular() {
		return nil, errors.New("outcome descriptor is not regular")
	}
	post, err := root.Lstat(name)
	if err != nil || !os.SameFile(descriptor, post) || (bound != nil && !os.SameFile(descriptor, bound)) {
		return nil, errors.New("outcome ledger changed while opening")
	}
	if err := validateOutcomeTarget(post); err != nil {
		return nil, err
	}
	return post, nil
}
