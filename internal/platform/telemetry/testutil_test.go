package telemetry

import (
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var providerInit sync.Once
var sharedProvider *sdktrace.TracerProvider

func testProvider() *sdktrace.TracerProvider {
	providerInit.Do(func() {
		sharedProvider = sdktrace.NewTracerProvider()
	})
	return sharedProvider
}
