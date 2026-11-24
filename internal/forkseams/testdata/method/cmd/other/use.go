package other

func use(v interface{ TakeSecret() string }) {
	_ = v.TakeSecret()
}
