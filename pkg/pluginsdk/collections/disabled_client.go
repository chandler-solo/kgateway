package collections

import (
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"istio.io/istio/pkg/kube/controllers"
	"istio.io/istio/pkg/kube/kclient"
	klabels "k8s.io/apimachinery/pkg/labels"
)

type disabledClient[T controllers.Object] struct{}

func NewDisabledClient[T controllers.Object]() kclient.Client[T] {
	return disabledClient[T]{}
}

func (disabledClient[T]) Get(string, string) T {
	var zero T
	return zero
}

func (disabledClient[T]) List(string, klabels.Selector) []T {
	return nil
}

func (disabledClient[T]) ListUnfiltered(string, klabels.Selector) []T {
	return nil
}

func (disabledClient[T]) AddEventHandler(cache.ResourceEventHandler) cache.ResourceEventHandlerRegistration {
	return disabledRegistration{}
}

func (disabledClient[T]) HasSynced() bool {
	return true
}

func (disabledClient[T]) HasSyncedIgnoringHandlers() bool {
	return true
}

func (disabledClient[T]) ShutdownHandlers() {}

func (disabledClient[T]) ShutdownHandler(cache.ResourceEventHandlerRegistration) {}

func (disabledClient[T]) Start(<-chan struct{}) {}

func (disabledClient[T]) Index(string, func(T) []string) kclient.RawIndexer {
	return disabledIndexer{}
}

func (disabledClient[T]) Create(T) (T, error) {
	var zero T
	return zero, nil
}

func (disabledClient[T]) Update(T) (T, error) {
	var zero T
	return zero, nil
}

func (disabledClient[T]) UpdateStatus(T) (T, error) {
	var zero T
	return zero, nil
}

func (disabledClient[T]) Patch(string, string, apitypes.PatchType, []byte) (T, error) {
	var zero T
	return zero, nil
}

func (disabledClient[T]) PatchStatus(string, string, apitypes.PatchType, []byte) (T, error) {
	var zero T
	return zero, nil
}

func (disabledClient[T]) ApplyStatus(string, string, apitypes.PatchType, []byte, string) (T, error) {
	var zero T
	return zero, nil
}

func (disabledClient[T]) Delete(string, string) error {
	return nil
}

type disabledRegistration struct{}

func (disabledRegistration) HasSynced() bool {
	return true
}

type disabledIndexer struct{}

func (disabledIndexer) Lookup(string) []any {
	return nil
}

var _ kclient.Client[controllers.Object] = disabledClient[controllers.Object]{}
