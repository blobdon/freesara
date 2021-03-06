package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/publicsuffix"
)

type offer struct {
	Title     string
	Location  string
	Summary   string
	ID        int
	Timestamp time.Time
	DaysOld   int
	URL       url.URL
}

type group struct {
	title  string
	url    url.URL
	offers []offer
}

var offers []offer
var wg sync.WaitGroup
var groups = []group{
	{title: "GreenwichUK"},
	{title: "CityOfLondon"},
	{title: "TowerHamletsUK"},
	{title: "LewishamUK"},
	{title: "SouthwarkUK"},
}
var tpl = template.Must(template.ParseFiles("tpl/index.html"))

// for use with Heroku
var fcUser, fcPass, port string

// for use with others
// var fcUser, fcPass string

func init() {
	fcUser = os.Getenv("FC_USER")
	fcPass = os.Getenv("FC_PASS")
	port = os.Getenv("PORT") // uncomment for Heroku
	for i := range groups {
		groups[i].prepURL("25")
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func (g *group) prepURL(numPosts string) {
	// http://groups.freecycle.org/IslingtonEastUK/posts/offer?resultsperpage=40
	g.url.Scheme = "https"
	g.url.Host = "groups.freecycle.org"
	g.url.Path = "/" + g.title + "/posts/offer"
	// g.url.Path += "/posts/offer"
	q := g.url.Query()
	q.Set("resultsperpage", numPosts)
	g.url.RawQuery = q.Encode()
}

func (g *group) getOffers(c *http.Client) {
	g.offers = nil // clear offers from previous request
	defer wg.Done()
	resp, err := c.Get(g.url.String())
	check(err)
	doc, err := goquery.NewDocumentFromResponse(resp)
	check(err)
	rows := doc.Find("#group_posts_table tr")
	rows.EachWithBreak(func(i int, tr *goquery.Selection) bool {
		o := offer{}
		td := tr.Find("td")
		alpha := "abcdefghijklmnopqrstuvwxyz"
		td1texts := td.First().Contents().FilterFunction(func(_ int, s *goquery.Selection) bool {
			if goquery.NodeName(s) == "#text" && strings.ContainsAny(s.Text(), alpha) {
				return true
			}
			return false
		})
		td2texts := td.Last().Contents().FilterFunction(func(_ int, s *goquery.Selection) bool {
			if goquery.NodeName(s) == "#text" && strings.ContainsAny(s.Text(), alpha) {
				return true
			}
			return false
		})
		td2a1 := td.Last().Find("a").First()
		o.Title = td2a1.Text()
		loc := td2texts.Eq(0).Text()
		// get everything between the () only, drop the byline
		lpar := strings.Index(loc, "(")
		rpar := strings.Index(loc, ")")
		if lpar < 0 || rpar < lpar+2 {
			o.Location = "UNKNOWN"
		} else {
			o.Location = loc[lpar+1 : rpar]
		}
		o.Summary = strings.TrimSpace(td2texts.Eq(1).Text())
		t := strings.TrimSpace(td1texts.Eq(0).Text())
		o.Timestamp, err = time.Parse("Mon Jan  2 15:04:05 2006", t)
		if err != nil {
			o.Timestamp = time.Now()
		}
		o.DaysOld = int(time.Since(o.Timestamp).Hours() / float64(24))
		if o.DaysOld > 14 {
			return false
		}
		if h, ok := td2a1.Attr("href"); ok {
			if u, err := url.Parse(h); err == nil {
				o.URL = *u
			} else {
				return false
			}
		} else {
			return false
		}
		// get id from url
		pre := "/posts/"
		prei := strings.Index(o.URL.Path, pre)
		idstring := o.URL.Path[prei+len(pre) : prei+len(pre)+8]
		if n, err := strconv.Atoi(idstring); err == nil {
			o.ID = n
		} else {
			o.ID = 0
		}
		g.offers = append(g.offers, o)
		return true
	})
}

func handler(w http.ResponseWriter, r *http.Request) {
	offers = nil
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	check(err)
	// On OSx and Heroku, freecycle's certficate chain is not recognized as valid
	// Instead of relying on the system cert pool, we can just include our own
	// copy of the CA certs we need for this application.
	// It is possible to append them to the system cert pool, but there is no
	// reason to do so, unless it was an easy default that met our needs.

	// Read in file containing the chain of certificates we want to trust
	certsFile := "freecycle-org-chain.pem"
	certs, err := ioutil.ReadFile(certsFile)
	if err != nil {
		log.Fatalf("failed to read freecycle certificate chain file %q: %v", certsFile, err)
	}

	// Add those certificates to an empty certificate pool.
	certPool := x509.NewCertPool()
	if ok := certPool.AppendCertsFromPEM(certs); !ok {
		log.Fatalln("failed to append freecycle certs to trusted cert pool")

	}

	// Configure the HTTP client to trust that cert pool.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: certPool},
		},
		Jar: jar,
	}

	fmt.Println("getting cookies")
	// get cookies from login page
	_, err = client.Get("https://my.freecycle.org")
	check(err)
	fmt.Println("logging in")
	// login
	_, err = client.PostForm("https://my.freecycle.org/login",
		url.Values{"username": {fcUser}, "pass": {fcPass}})
	check(err)
	fmt.Println("getting offers")
	for i := range groups {
		fmt.Println("getting offers from group", i, "at", groups[i].url.String())
		wg.Add(1) // wg.Done inside getOffers()
		go groups[i].getOffers(client)
	}
	wg.Wait()
	// this should be done in a channel as part of above loop,
	// can't put in getOffers b/c of concurrency problem of some sort
	for _, g := range groups {
		offers = append(offers, g.offers...)
	}
	fmt.Println("sorting offers")
	sort.Slice(offers, func(i, j int) bool {
		return offers[i].Timestamp.After(offers[j].Timestamp)
	})
	fmt.Println("templating")
	w.Header().Set("Content-Type", "text/html")
	tpl.Execute(w, offers)
	fmt.Println("Done")
}

func main() {

	http.HandleFunc("/", handler)
	http.Handle("/tpl/", http.FileServer(http.Dir("./")))

	log.Fatal(http.ListenAndServe(":"+port, nil)) // for Heroku
	// http.ListenAndServe(":8080", nil) // for others
}
