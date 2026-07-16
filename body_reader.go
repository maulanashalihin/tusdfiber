package tusdfiber

import (
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// bodyReader wraps an io.Reader (from fasthttp's request body stream) to track
// errors and byte counts. It is used during PATCH requests to read upload chunks.
type bodyReader struct {
	bytesCounter int64
	reader       io.ReadCloser
	lock         sync.RWMutex
	err          error
}

func newBodyReader(reader io.ReadCloser, maxSize int64) *bodyReader {
	limited := io.LimitReader(reader, maxSize)
	// We need a clean way to detect when the limit is hit.
	// io.LimitReader returns EOF when the limit is reached, which is fine.
	return &bodyReader{
		reader: io.NopCloser(limited),
	}
}

func (r *bodyReader) Read(b []byte) (int, error) {
	r.lock.RLock()
	hasErrored := r.err != nil
	r.lock.RUnlock()
	if hasErrored {
		return 0, io.EOF
	}

	n, err := r.reader.Read(b)
	atomic.AddInt64(&r.bytesCounter, int64(n))

	if err != nil {
		if err == io.EOF {
			return n, io.EOF
		}

		// Categorise common errors
		if err == io.ErrClosedPipe || err == io.ErrUnexpectedEOF {
			err = ErrUnexpectedEOF
		}
		if strings.Contains(err.Error(), "connection reset by peer") {
			err = ErrConnectionReset
		}

		r.lock.Lock()
		if r.err == nil {
			r.err = err
		}
		r.lock.Unlock()
	}

	return n, nil
}

func (r *bodyReader) hasError() error {
	r.lock.RLock()
	err := r.err
	r.lock.RUnlock()
	if err == io.EOF {
		return nil
	}
	return err
}

func (r *bodyReader) bytesRead() int64 {
	return atomic.LoadInt64(&r.bytesCounter)
}

func (r *bodyReader) closeWithError(err error) {
	r.lock.Lock()
	r.err = err
	r.lock.Unlock()
	r.reader.Close()
}

// isLimitReached returns true if the error was caused by hitting the max-size limit.
func isLimitReached(err error) bool {
	if err == nil {
		return false
	}
	// io.LimitReader does not return a specific "limit reached" error;
	// it returns EOF. We detect it by checking if the body reader hit EOF
	// while we expected more data. The caller handles this via ErrSizeExceeded.
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
