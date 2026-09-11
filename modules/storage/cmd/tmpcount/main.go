package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", os.Args[1])
	if err != nil {
		panic(err)
	}
	defer db.Close()
	for _, pragma := range []string{"page_count", "freelist_count", "page_size"} {
		var value int64
		if err := db.QueryRow("pragma " + pragma).Scan(&value); err == nil {
			fmt.Printf("%s=%d\n", pragma, value)
		}
	}
	rows, err := db.Query(`select name from sqlite_master where type='table' order by name`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			panic(err)
		}
		var count int64
		if err := db.QueryRow("select count(*) from \"" + name + "\"").Scan(&count); err != nil {
			fmt.Printf("%s err=%v\n", name, err)
			continue
		}
		fmt.Printf("%s=%d\n", name, count)
	}
}
