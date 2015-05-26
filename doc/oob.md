# Out Of Band protocol specification

## Frame Format

Commands sent from the client to the server or the server to the client follow
the same frame format:

```
  frame = header, {field}, "end".
  header = command, " ", framelength, " ", seqno, "\n".
  framelength = tendigit.
  seqno = tendigit.
  tendigit = digit, digit, digit, digit, digit,
             digit, digit, digit, digit, digit.
  digit = "0" | "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9".
  command = "publ"  (* publish to a uri               *) |
            "pers"  (* persist to a uri               *) |
            "subs"  (* subscribe to a uri             *) |
            "list"  (* list the children of a URI     *) |
            "quer"  (* query a given URI              *) |
            "tsub"  (* tap subscribe a URI            *) |
            "tque"  (* tap query a given URI          *) |
            "putd"  (* put a dot to a router          *) |
            "pute"  (* put an entity to a router      *) |
            "putc"  (* put a chain to a router        *) |
            "makd"  (* make a dot                     *) |
            "make"  (* make an entity                 *) |
            "makc"  (* make a chain                   *) |
            "bldc"  (* build a chain                  *) |
            "adpd"  (* add a preferred dot            *) |
            "adpc"  (* add a preferred chain          *) |
            "dlpd"  (* delete a preferred dot         *) |
            "dlpc"  (* delete a preferred chain       *) |
            "sete"  (* set the entity the client uses *).
  field = KVfield | POfield | ROfield.
  fieldlen = digit, {digit}.
  keychar = "a"|"b"|"c"|"d"|"e"|"f"|"g"|"h"|"i"|"j"|"k"|"l"|
            "m"|"n"|"o"|"p"|"q"|"r"|"s"|"t"|"u"|"v"|"w"|"x"|
            "y"|"z"|"0"|"1"|"2"|"3"|"4"|"5"|"6"|"7"|"8"|"9"|"_".
  onetwo = "1" | "2".
  octet = [onetwo], [digit], digit.
  dotform = octet, ".", octet, ".", octet, ".", octet.
  ponum = digit, {digit}.
  key = keychar, {keychar}.
  KVfield = "kv ", key, " ", fieldlen, "\n", BLOB, "\n".
  POtype = POtypedot | POtypenum | POtypeboth.
  POtypedot = dotform,":".
  POtypenum = ":",ponum.
  POtypeboth = dotform, ":", ponum.
  POfield = "po ", POtype, " ", fieldlen, "\n", BLOB, "\n".
  ROfield = "ro ", octet, " ", fieldlen, "\n", BLOB, "\n".
```

## Overview

The frame header line is exactly 27 bytes, which enables reading it without parsing.
Once read, the length field can be used to read the whole frame, or the frame can be
parsed field by field. The router does not use the length field in reading the
frames it receives, so it may be set to zero for frames originating from the client.

The sequence number is a random unique 31 bit number that is used to connect replies
to commands. Any response or result attached to the command will have the same
sequence number, so it can be used to demultiplex on the client side.

## Commands

### sete - SetEntity
Fields:
* REQ po(1.0.1.2) - the signing entity to use

This sets the entity that is represented by the connected client. All DOTs are
generated from this entity, and messages are signed using its key.

### publ - Publish
Fields:
* REQ kv(uri) - the URI to publish to. Can be given split as kv(mvk) and kv(uri_suffix)
* kv(primary_access_chain) - the hash of the primary access DOT chain to use
* kv(expiry) - the date in RFC3339 format for the message to expire
* kv(expirydelta) - the duration after now for the message to expire. Allowable suffixes include ms,s,m,h
* kv(elaborate_pac) - the elaboration level for the PAC. Allowable values are "partial" or "full". Omitting results in no elaboration.
* ro(*) - will be included
* po(*) - will be included

This publishes a message to the given uri. A single `resp` frame will be
delivered with the same sequence number to convey the success or failure of the
publish operation

### subs - Subscribe
Fields:
* REQ kv(uri) - the URI to subscribe to. Can be given split as kv(mvk) and kv(uri_suffix)
* kv(primary_access_chain) - the hash of the primary access DOT chain to use
* kv(expiry) - the date in RFC3339 format for the subscribe request to expire
* kv(expirydelta) - the duration after now for the subscribe request to expire. Allowable suffixes include ms,s,m,h
* kv(elaborate_pac) - the elaboration level for the PAC. Allowable values are "partial" or "full". Omitting results in no elaboration.
* kv(unpack) - boolean: should the matching messages be unpacked
* ro(*) - will be included

This subscribes to the given URI. A single `resp` frame will be delivered
with the same sequence number to convey the success or failure of the subscribe
operation. A `rslt` frame will be delivered for every message matching the
subscription, if the `resp` frame indicated success. If `unpack` was specified,
then the messages will be unpacked into their constituent ROs and POs.

### pers - Persist
A persist frame is exactly the same as a publish frame.

### list - List
Fields:
* REQ kv(uri) - the URI to list. Can be given split as kv(mvk) and kv(uri_suffix)
* kv(primary_access_chain) - the hash of the primary access DOT chain to use
* kv(expiry) - the date in RFC3339 format for the list request to expire
* kv(expirydelta) - the duration after now for the list request to expire. Allowable suffixes include ms,s,m,h
* kv(elaborate_pac) - the elaboration level for the PAC. Allowable values are "partial" or "full". Omitting results in no elaboration.
* ro(*) - will be included

This lists the children of the given URI. A single `resp` frame will be delivered
with the same sequence number to convey the success or failure of the operation.
A `rslt` frame will be delivered for every child. The result frame will contain
two fields: kv(finished) which will be "true" if there are no more results, or
"false" if there are more results. If "false", there will also be kv("child")
containing the full URI of the child.

### quer - Query
Fields:
* REQ kv(uri) - the URI to query. Can be given split as kv(mvk) and kv(uri_suffix)
* kv(primary_access_chain) - the hash of the primary access DOT chain to use
* kv(expiry) - the date in RFC3339 format for the query request to expire
* kv(expirydelta) - the duration after now for the query request to expire. Allowable suffixes include ms,s,m,h
* kv(elaborate_pac) - the elaboration level for the PAC. Allowable values are "partial" or "full". Omitting results in no elaboration.
* kv(unpack) - boolean: should the matching messages be unpacked
* ro(*) - will be included

This queries the given URI. A single `resp` frame will be delivered
with the same sequence number to convey the success or failure of the operation.
If `resp` indicated success, a `rslt` frame will be delivered for every message
matching the query. If `unpack` was specified, then the matching messages will
be unpacked into their constituent ROs and POs.

### tsub - Tap Subscribe
A tap subscribe frame is the same as a subscribe frame. It is not currently implemented

### tque - Tap Query
A tap query frame is the same as a query frame. It is not currently implemented
