// Code generated by mockery v1.0.0. DO NOT EDIT.

package oapispecmocks

import (
	context "context"

	fftypes "github.com/hyperledger/firefly/pkg/fftypes"
	mock "github.com/stretchr/testify/mock"

	openapi3 "github.com/getkin/kin-openapi/openapi3"
)

// FFISwaggerGen is an autogenerated mock type for the FFISwaggerGen type
type FFISwaggerGen struct {
	mock.Mock
}

// Generate provides a mock function with given fields: ctx, baseURL, ffi
func (_m *FFISwaggerGen) Generate(ctx context.Context, baseURL string, ffi *fftypes.FFI) (*openapi3.T, error) {
	ret := _m.Called(ctx, baseURL, ffi)

	var r0 *openapi3.T
	if rf, ok := ret.Get(0).(func(context.Context, string, *fftypes.FFI) *openapi3.T); ok {
		r0 = rf(ctx, baseURL, ffi)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*openapi3.T)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(context.Context, string, *fftypes.FFI) error); ok {
		r1 = rf(ctx, baseURL, ffi)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}