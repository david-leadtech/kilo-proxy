package main

func (a *app) closeT3CodeAssets() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.t3CodeAssets != nil {
		a.t3CodeAssets.Close()
		a.t3CodeAssets = nil
	}
}
