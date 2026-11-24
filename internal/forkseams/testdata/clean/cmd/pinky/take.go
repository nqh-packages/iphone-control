package main

func take(v interface{ TakeSecret() string }) { _ = v.TakeSecret() }
