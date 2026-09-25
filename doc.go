// Package file is the `file` module: entities, edges and series read from CSV and JSON files
// through a mapping in config, reloaded when the files change.
//
//	modules:
//	  - kind: file
//	    name: inventory
//	    options:
//	      files:
//	        - path: ~/fleet/hosts.csv       # format from the extension: .csv, .tsv or .json
//	          entities:
//	            kind: host
//	            id: hostname                # the field holding each entity's id; required
//	            name: display               # default: the id
//	            status: state               # ok, warn, crit, down or unknown
//	            reason: note
//	            attrs: [os, cores]
//	            tags: tags
//	            edges:
//	              - {rel: depends_on, to: depends, kind: host}
//	        - path: ~/fleet/services.json
//	          records: data.services        # dotted path to the array of records
//	          entities: {kind: service, id: id, attrs: [meta.owner]}
//	        - path: ~/fleet/cpu.csv
//	          series:
//	            kind: host
//	            id: host
//	            time: ts                    # RFC 3339, or Unix seconds
//	            metrics:
//	              cpu.utilisation: {field: cpu, unit: percent}
//	        - path: ~/fleet/changes.csv
//	          events:
//	            kind: host                  # with id: the entity each event is about
//	            id: host                    # an empty cell makes a global event
//	            time: ts
//	            severity: level             # debug, info, warn, error or critical; default info
//	            type: what                  # e.g. deploy or alert; default "event"
//	            message: text               # required
//	            fields: [version]
//
// CSV fields are header names; JSON fields are keys, dotted to reach nested objects. A CSV
// cell holding several values (tags, edge targets) separates them with ';'; JSON uses arrays.
// Series ids with no entity of their own become bare entities. Malformed rows are skipped and
// reported once as warning events naming the file and line; a missing or unreadable file shows
// in Health, and its entities are removed.
package file
