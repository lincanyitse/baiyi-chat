package web

import (
	"context"
	"sync"

	"github.com/lincanyitse/baiyi-chat/internal/secret"
)

// fakeSecrets 是内存版密钥存储，测试用。
type fakeSecrets struct {
	mu          sync.Mutex
	m           map[string]string
	protection  secret.Protection
	resetCalled bool
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{
		m:          make(map[string]string),
		protection: secret.ProtectionDPAPI,
	}
}

func (f *fakeSecrets) Put(_ context.Context, name, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key == "" {
		delete(f.m, name)
		return nil
	}
	f.m[name] = key
	return nil
}

func (f *fakeSecrets) Get(_ context.Context, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[name]
	return v, ok, nil
}

func (f *fakeSecrets) Has(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[name]
	return ok, nil
}

func (f *fakeSecrets) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, name)
	return nil
}

func (f *fakeSecrets) Reset(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m = make(map[string]string)
	f.resetCalled = true
	return nil
}

func (f *fakeSecrets) Protection() secret.Protection { return f.protection }
