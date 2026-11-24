package other

func use(g interface{ markCredentialTyped() }) {
	g.markCredentialTyped()
}
