package acme

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// Logger is used to log errors; if nil, the default log.Logger is used.
var Logger *log.Logger

// logger is an helper function to retrieve the available logger
func logger() *log.Logger {
	if Logger == nil {
		Logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	return Logger
}

// User interface is to be implemented by users of this library.
// It is used by the client type to get user specific information.
type User interface {
	GetEmail() string
	GetRegistration() *RegistrationResource
	GetPrivateKey() *rsa.PrivateKey
}

type solver interface {
	CanSolve() bool
	Solve(challenge challenge, domain string)
}

// Client is the user-friendy way to ACME
type Client struct {
	regURL  string
	user    User
	jws     *jws
	Solvers map[string]solver
}

// NewClient creates a new client for the set user.
func NewClient(caURL string, usr User, optPort string) *Client {
	if err := usr.GetPrivateKey().Validate(); err != nil {
		logger().Fatalf("Could not validate the private account key of %s -> %v", usr.GetEmail(), err)
	}

	jws := &jws{privKey: usr.GetPrivateKey()}

	// REVIEW: best possibility?
	solvers := make(map[string]solver)
	solvers["simpleHttp"] = &simpleHTTPChallenge{jws: jws}
	solvers["dvsni"] = &dvsniChallenge{}

	return &Client{regURL: caURL, user: usr, jws: jws}
}

// Register the current account to the ACME server.
func (c *Client) Register() (*RegistrationResource, error) {
	logger().Print("Registering account ... ")
	jsonBytes, err := json.Marshal(registrationMessage{Contact: []string{"mailto:" + c.user.GetEmail()}})
	if err != nil {
		return nil, err
	}

	resp, err := c.jws.post(c.regURL, jsonBytes)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusConflict {
		// REVIEW: should this return an error?
		return nil, errors.New("This account is already registered with this CA.")
	}

	var serverReg Registration
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&serverReg)
	if err != nil {
		return nil, err
	}

	reg := &RegistrationResource{Body: serverReg}

	links := parseLinks(resp.Header["Link"])
	reg.URI = resp.Header.Get("Location")
	if links["terms-of-service"] != "" {
		reg.TosURL = links["terms-of-service"]
	}

	if links["next"] != "" {
		reg.NewAuthzURL = links["next"]
	} else {
		return nil, errors.New("The server did not return enough information to proceed...")
	}

	return reg, nil
}

// AgreeToTos updates the Client registration and sends the agreement to
// the server.
func (c *Client) AgreeToTos() error {
	c.user.GetRegistration().Body.Agreement = c.user.GetRegistration().TosURL
	jsonBytes, err := json.Marshal(&c.user.GetRegistration().Body)
	if err != nil {
		return err
	}

	logger().Printf("Agreement: %s", string(jsonBytes))

	resp, err := c.jws.post(c.user.GetRegistration().URI, jsonBytes)
	if err != nil {
		return err
	}

	logResponseBody(resp)

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("The server returned %d but we expected %d", resp.StatusCode, http.StatusAccepted)
	}

	logResponseHeaders(resp)
	logResponseBody(resp)

	return nil
}

// ObtainCertificates tries to obtain certificates from the CA server
// using the challenges it has configured. It also tries to do multiple
// certificate processings at the same time in parallel.
func (c *Client) ObtainCertificates(domains []string) error {

	challenges := c.getChallenges(domains)
	c.solveChallenges(challenges)
	return nil
}

// Looks through the challenge combinations to find a solvable match.
// Then solves the challenges in series and returns.
func (c *Client) solveChallenges(challenges []*authorizationResource) error {
	// loop through the resources, basically through the domains.
	for _, authz := range challenges {
		// no solvers - no solving
		if solvers := c.chooseSolvers(authz.Body); solvers != nil {
			for i, solver := range solvers {
				solver.Solve(authz.Body.Challenges[i], authz.Domain)
			}
		} else {
			return fmt.Errorf("Could not determine solvers for %s", authz.Domain)
		}
	}

	return nil
}

// Checks all combinations from the server and returns an array of
// solvers which should get executed in series.
func (c *Client) chooseSolvers(auth authorization) map[int]solver {
	for _, combination := range auth.Combinations {
		solvers := make(map[int]solver)
		for i := range combination {
			if solver, ok := c.Solvers[auth.Challenges[i].Type]; ok {
				solvers[i] = solver
			}
		}

		// If we can solve the whole combination, return the solvers
		if len(solvers) == len(combination) {
			return solvers
		}
	}
	return nil
}

// Get the challenges needed to proof our identifier to the ACME server.
func (c *Client) getChallenges(domains []string) []*authorizationResource {
	resc, errc := make(chan *authorizationResource), make(chan error)

	for _, domain := range domains {
		go func(domain string) {
			jsonBytes, err := json.Marshal(authorization{Identifier: identifier{Type: "dns", Value: domain}})
			if err != nil {
				errc <- err
				return
			}

			resp, err := c.jws.post(c.user.GetRegistration().NewAuthzURL, jsonBytes)
			if err != nil {
				errc <- err
				return
			}

			if resp.StatusCode != http.StatusCreated {
				errc <- fmt.Errorf("Getting challenges for %s failed. Got status %d but expected %d",
					domain, resp.StatusCode, http.StatusCreated)
			}

			links := parseLinks(resp.Header["Link"])
			if links["next"] == "" {
				logger().Fatalln("The server did not provide enough information to proceed.")
			}

			var authz authorization
			decoder := json.NewDecoder(resp.Body)
			err = decoder.Decode(&authz)
			if err != nil {
				errc <- err
			}

			resc <- &authorizationResource{Body: authz, NewCertURL: links["next"], Domain: domain}

		}(domain)
	}

	var responses []*authorizationResource
	for i := 0; i < len(domains); i++ {
		select {
		case res := <-resc:
			responses = append(responses, res)
		case err := <-errc:
			logger().Printf("%v", err)
		}
	}

	close(resc)
	close(errc)

	return responses
}

func logResponseHeaders(resp *http.Response) {
	logger().Println(resp.Status)
	for k, v := range resp.Header {
		logger().Printf("-- %s: %s", k, v)
	}
}

func logResponseBody(resp *http.Response) {
	body, _ := ioutil.ReadAll(resp.Body)
	logger().Printf("Returned json data: \n%s", body)
}

func parseLinks(links []string) map[string]string {
	aBrkt := regexp.MustCompile("[<>]")
	slver := regexp.MustCompile("(.+) *= *\"(.+)\"")
	linkMap := make(map[string]string)

	for _, link := range links {

		link = aBrkt.ReplaceAllString(link, "")
		parts := strings.Split(link, ";")

		matches := slver.FindStringSubmatch(parts[1])
		if len(matches) > 0 {
			linkMap[matches[2]] = parts[0]
		}
	}

	return linkMap
}
