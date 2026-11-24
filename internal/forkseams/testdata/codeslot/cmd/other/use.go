package other

func use(g interface{ takeCode() []byte }) {
	_ = g.takeCode()
}
