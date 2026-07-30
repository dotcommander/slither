package slither

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func streamOutcomeLedger(ctx context.Context, path string, partialTrailingRecords *int, apply func(OutcomeRecord) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open outcomes: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, terminated, oversized, err := readOutcomeLine(reader)
		if err == io.EOF && len(line) == 0 && !oversized {
			return nil
		}
		if oversized {
			if err == io.EOF && !terminated {
				*partialTrailingRecords = *partialTrailingRecords + 1
				return nil
			}
			return errors.New("outcome record exceeds 1 MiB")
		}
		if len(bytes.TrimSpace(line)) != 0 {
			record, _, decodeErr := decodeOutcomeRecord(line)
			if decodeErr != nil {
				if err == io.EOF && !terminated && isTruncatedOutcomeError(decodeErr) {
					*partialTrailingRecords = *partialTrailingRecords + 1
					return nil
				}
				return fmt.Errorf("decode outcome ledger: %w", decodeErr)
			}
			if validateErr := validateStoredOutcomeRecord(record); validateErr != nil {
				return validateErr
			}
			if applyErr := apply(record); applyErr != nil {
				return applyErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read outcome ledger: %w", err)
		}
	}
}

func isTruncatedOutcomeError(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected end of JSON input")
}

func readOutcomeLine(reader *bufio.Reader) ([]byte, bool, bool, error) {
	var line []byte
	overflow := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			terminated := fragment[len(fragment)-1] == '\n'
			if terminated {
				fragment = fragment[:len(fragment)-1]
				if len(fragment) > 0 && fragment[len(fragment)-1] == '\r' {
					fragment = fragment[:len(fragment)-1]
				}
			}
			if !overflow && len(line)+len(fragment) < maxOutcomeRecordLen {
				line = append(line, fragment...)
			} else {
				overflow = true
			}
			if terminated {
				return line, true, overflow, nil
			}
		}
		switch err {
		case nil:
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return line, false, overflow, io.EOF
		default:
			return nil, false, false, err
		}
	}
}
