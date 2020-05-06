package main

import (
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	r "github.com/dancannon/gorethink"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

func main() {
	var (
		addr string = "localhost:3000"
	)

	flag.StringVar(&addr, "addr", "localhost:3000", "")
	flag.Parse()

	server := NewServer(addr)
	StartServer(server)
}

var (
	router  *mux.Router
	session *r.Session
)

func init() {
	var err error

	session, err = r.Connect(r.ConnectOpts{
		Address:  "localhost:28015",
		Database: "todo",
		MaxOpen:  40,
	})
	if err != nil {
		log.Fatalln(err.Error())
	}
}

func NewServer(addr string) *http.Server {
	// Setup router
	router = initRouting()

	// Create and start server
	return &http.Server{
		Addr:    addr,
		Handler: router,
	}
}

func StartServer(server *http.Server) {
	log.Println("Starting server at http://localhost:3000")
	err := server.ListenAndServe()
	if err != nil {
		log.Fatalln("Error: %v", err)
	}
}

func initRouting() *mux.Router {

	r := mux.NewRouter()

	r.HandleFunc("/", indexHandler)
	r.HandleFunc("/all", indexHandler)
	r.HandleFunc("/active", activeIndexHandler)
	r.HandleFunc("/completed", completedIndexHandler)
	r.HandleFunc("/new", newHandler)
	r.HandleFunc("/toggle/{id}", toggleHandler)
	r.HandleFunc("/delete/{id}", deleteHandler)
	r.HandleFunc("/clear", clearHandler)

	// Add handler for websocket server
	r.Handle("/ws/all", newChangesHandler(allChanges))
	r.Handle("/ws/active", newChangesHandler(activeChanges))
	r.Handle("/ws/completed", newChangesHandler(completedChanges))

	// Add handler for static files
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("static")))

	return r
}

func newChangesHandler(fn func(chan interface{})) http.HandlerFunc {
	h := newHub()
	go h.run()

	fn(h.broadcast)

	return wsHandler(h)
}

// Handlers

func indexHandler(w http.ResponseWriter, req *http.Request) {
	items := []TodoItem{}

	// Fetch all the items from the database
	res, err := r.Table("items").OrderBy(r.Asc("Created")).Run(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = res.All(&items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	renderTemplate(w, "index", map[string]interface{}{
		"Items": items,
		"Route": "all",
	})
}

func activeIndexHandler(w http.ResponseWriter, req *http.Request) {
	items := []TodoItem{}

	// Fetch all the items from the database
	query := r.Table("items").Filter(r.Row.Field("Status").Eq("active"))
	query = query.OrderBy(r.Asc("Created"))
	res, err := query.Run(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = res.All(&items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	renderTemplate(w, "index", map[string]interface{}{
		"Items": items,
		"Route": "active",
	})
}

func completedIndexHandler(w http.ResponseWriter, req *http.Request) {
	items := []TodoItem{}

	// Fetch all the items from the database
	query := r.Table("items").Filter(r.Row.Field("Status").Eq("complete"))
	query = query.OrderBy(r.Asc("Created"))
	res, err := query.Run(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = res.All(&items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	renderTemplate(w, "index", map[string]interface{}{
		"Items": items,
		"Route": "completed",
	})
}

func newHandler(w http.ResponseWriter, req *http.Request) {
	// Create the item
	item := NewTodoItem(req.PostFormValue("text"))
	item.Created = time.Now()

	// Insert the new item into the database
	_, err := r.Table("items").Insert(item).RunWrite(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, req, "/", http.StatusFound)
}

func toggleHandler(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	id := vars["id"]
	if id == "" {
		http.NotFound(w, req)
		return
	}

	// Check that the item exists
	res, err := r.Table("items").Get(id).Run(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if res.IsNil() {
		http.NotFound(w, req)
		return
	}

	// Toggle the item
	_, err = r.Table("items").Get(id).Update(map[string]interface{}{"Status": r.Branch(
		r.Row.Field("Status").Eq("active"),
		"complete",
		"active",
	)}).RunWrite(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, req, "/", http.StatusFound)
}

func deleteHandler(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	id := vars["id"]
	if id == "" {
		http.NotFound(w, req)
		return
	}

	// Check that the item exists
	res, err := r.Table("items").Get(id).Run(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if res.IsNil() {
		http.NotFound(w, req)
		return
	}

	// Delete the item
	_, err = r.Table("items").Get(id).Delete().RunWrite(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, req, "/", http.StatusFound)
}

func clearHandler(w http.ResponseWriter, req *http.Request) {
	// Delete all completed items
	_, err := r.Table("items").Filter(
		r.Row.Field("Status").Eq("complete"),
	).Delete().RunWrite(session)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, req, "/", http.StatusFound)
}

func allChanges(ch chan interface{}) {
	// Use goroutine to wait for changes. Prints the first 10 results
	go func() {
		for {
			res, err := r.DB("todo").Table("items").Changes().Run(session)
			if err != nil {
				log.Fatalln(err)
			}

			var response interface{}
			for res.Next(&response) {
				ch <- response
			}

			if res.Err() != nil {
				log.Println(res.Err())
			}
		}
	}()
}
func activeChanges(ch chan interface{}) {
	// Use goroutine to wait for changes. Prints the first 10 results
	go func() {
		for {
			res, err := r.DB("todo").Table("items").Filter(r.Row.Field("Status").Eq("active")).Changes().Run(session)
			if err != nil {
				log.Fatalln(err)
			}

			var response interface{}
			for res.Next(&response) {
				ch <- response
			}

			if res.Err() != nil {
				log.Println(res.Err())
			}
		}
	}()
}
func completedChanges(ch chan interface{}) {
	// Use goroutine to wait for changes. Prints the first 10 results
	go func() {
		for {
			res, err := r.DB("todo").Table("items").Filter(r.Row.Field("Status").Eq("complete")).Changes().Run(session)
			if err != nil {
				log.Fatalln(err)
			}

			var response interface{}
			for res.Next(&response) {
				ch <- response
			}

			if res.Err() != nil {
				log.Println(res.Err())
			}
		}
	}()
}

var templates *template.Template

func init() {
	filenames := []string{}
	err := filepath.Walk("templates", func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() && filepath.Ext(path) == ".gohtml" {
			filenames = append(filenames, path)
		}

		return nil
	})
	if err != nil {
		log.Fatalln(err)
	}

	if len(filenames) == 0 {
		return
	}

	templates, err = template.ParseFiles(filenames...)
	if err != nil {
		log.Fatalln(err)
	}
}

func renderTemplate(w http.ResponseWriter, tmpl string, vars interface{}) {
	err := templates.ExecuteTemplate(w, tmpl+".gohtml", vars)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type connection struct {
	// The websocket connection.
	ws *websocket.Conn

	// Buffered channel of outbound messages.
	send chan interface{}
}

func (c *connection) reader() {
	for {
		_, _, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
	}
	c.ws.Close()
}

func (c *connection) writer() {
	for change := range c.send {
		err := c.ws.WriteJSON(change)
		if err != nil {
			break
		}
	}
	c.ws.Close()
}

var upgrader = &websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024}

func wsHandler(h hub) http.HandlerFunc {
	log.Println("Starting websocket server")
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &connection{send: make(chan interface{}, 256), ws: ws}
		h.register <- c
		defer func() { h.unregister <- c }()
		go c.writer()
		c.reader()
	}
}

type hub struct {
	// Registered connections.
	connections map[*connection]bool

	// Inbound messages from the connections.
	broadcast chan interface{}

	// Register requests from the connections.
	register chan *connection

	// Unregister requests from connections.
	unregister chan *connection
}

func newHub() hub {
	return hub{
		broadcast:   make(chan interface{}),
		register:    make(chan *connection),
		unregister:  make(chan *connection),
		connections: make(map[*connection]bool),
	}
}

func (h *hub) run() {
	for {
		select {
		case c := <-h.register:
			h.connections[c] = true
		case c := <-h.unregister:
			if _, ok := h.connections[c]; ok {
				delete(h.connections, c)
				close(c.send)
			}
		case m := <-h.broadcast:
			for c := range h.connections {
				select {
				case c.send <- m:
				default:
					delete(h.connections, c)
					close(c.send)
				}
			}
		}
	}
}

type TodoItem struct {
	Id      string `gorethink:"id,omitempty"`
	Text    string
	Status  string
	Created time.Time
}

func (t *TodoItem) Completed() bool {
	return t.Status == "complete"
}

func NewTodoItem(text string) *TodoItem {
	return &TodoItem{
		Text:   text,
		Status: "active",
	}
}
