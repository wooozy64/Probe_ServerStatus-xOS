package rpc

import (
	"errors"
	"io"
	"sync/atomic"
	"time"
)

type ioStreamContext struct {
	userIo           io.ReadWriteCloser
	agentIo          io.ReadWriteCloser
	userIoConnectCh  chan struct{}
	agentIoConnectCh chan struct{}
}

func (s *SeverHandler) CreateStream(streamId string) {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	s.ioStreams[streamId] = &ioStreamContext{
		userIoConnectCh:  make(chan struct{}),
		agentIoConnectCh: make(chan struct{}),
	}
}

func (s *SeverHandler) GetStream(streamId string) (*ioStreamContext, error) {
	s.ioStreamMutex.RLock()
	defer s.ioStreamMutex.RUnlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		return ctx, nil
	}

	return nil, errors.New("stream not found")
}

func (s *SeverHandler) CloseStream(streamId string) error {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		if ctx.userIo != nil {
			ctx.userIo.Close()
		}
		if ctx.agentIo != nil {
			ctx.agentIo.Close()
		}
		delete(s.ioStreams, streamId)
	}

	return nil
}

func (s *SeverHandler) UserConnected(streamId string, userIo io.ReadWriteCloser) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	stream.userIo = userIo
	close(stream.userIoConnectCh)

	return nil
}

func (s *SeverHandler) AgentConnected(streamId string, agentIo io.ReadWriteCloser) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	stream.agentIo = agentIo
	close(stream.agentIoConnectCh)

	return nil
}

func (s *SeverHandler) StartStream(streamId string, timeout time.Duration) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	timeoutTimer := time.NewTimer(timeout)

LOOP:
	for {
		select {
		case <-stream.userIoConnectCh:
			if stream.agentIo != nil {
				timeoutTimer.Stop()
				break LOOP
			}
		case <-stream.agentIoConnectCh:
			if stream.userIo != nil {
				timeoutTimer.Stop()
				break LOOP
			}
		case <-time.After(timeout):
			break LOOP
		}
		time.Sleep(time.Millisecond * 500)
	}

	if stream.userIo == nil && stream.agentIo == nil {
		return errors.New("timeout: no connection established")
	}
	if stream.userIo == nil {
		return errors.New("timeout: user connection not established")
	}
	if stream.agentIo == nil {
		return errors.New("timeout: agent connection not established")
	}

	isDone := new(atomic.Bool)
	endCh := make(chan struct{})

	go func() {
		_, innerErr := io.CopyBuffer(stream.userIo, stream.agentIo, make([]byte, 1048576))
		if innerErr != nil {
			err = innerErr
		}
		if isDone.CompareAndSwap(false, true) {
			close(endCh)
		}
	}()
	go func() {
		_, innerErr := io.CopyBuffer(stream.agentIo, stream.userIo, make([]byte, 1048576))
		if innerErr != nil {
			err = innerErr
		}
		if isDone.CompareAndSwap(false, true) {
			close(endCh)
		}
	}()

	<-endCh
	return err
}
