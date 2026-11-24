package main

func ok(v interface{ TakeSecret() string }) { _ = v.TakeSecret() }
