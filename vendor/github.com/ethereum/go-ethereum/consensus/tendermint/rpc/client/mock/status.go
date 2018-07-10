package mock

import (
	"github.com/ethereum/go-ethereum/consensus/tendermint/rpc/client"
	ctypes "github.com/ethereum/go-ethereum/consensus/tendermint/rpc/core/types"
)

// StatusMock returns the result specified by the Call
type StatusMock struct {
	Call
}

func (m *StatusMock) _assertStatusClient() client.StatusClient {
	return m
}

func (m *StatusMock) Status() (*ctypes.ResultStatus, error) {
	res, err := m.GetResponse(nil)
	if err != nil {
		return nil, err
	}
	return res.(*ctypes.ResultStatus), nil
}

// StatusRecorder can wrap another type (StatusMock, full client)
// and record the status calls
type StatusRecorder struct {
	Client client.StatusClient
	Calls  []Call
}

func NewStatusRecorder(client client.StatusClient) *StatusRecorder {
	return &StatusRecorder{
		Client: client,
		Calls:  []Call{},
	}
}

func (r *StatusRecorder) _assertStatusClient() client.StatusClient {
	return r
}

func (r *StatusRecorder) addCall(call Call) {
	r.Calls = append(r.Calls, call)
}

func (r *StatusRecorder) Status() (*ctypes.ResultStatus, error) {
	res, err := r.Client.Status()
	r.addCall(Call{
		Name:     "status",
		Response: res,
		Error:    err,
	})
	return res, err
}