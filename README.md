# Go RethinkDB setup demo

- docker run -d -p 8080:8080 -p 28015:28015 -p 29015:29015 rethinkdb

To run the code you should have RethinkDB installed with the database `todo` and the table `items`. You should then run the following command `go build && ./GoRethink_TodoDemo` and navigate to `http://localhost:3000`.

