package acme

type simpleHTTPChallenge struct{}

func (s *simpleHTTPChallenge) CanSolve() bool {
	return true
}

func (s *simpleHTTPChallenge) Solve(challenge challenge) {
func (s *simpleHTTPChallenge) Solve(chlng challenge, domain string) {

}
