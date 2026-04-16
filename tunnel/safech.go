// Copyright 2026 Panagiotis Karagiannis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tunnel

import "log"

// safeSend attempts to send to a channel without blocking.
// Returns true if send succeeded, false if channel was full.
// Logs a warning if the send would block.
func safeSend[T any](ch chan T, value T, name string) bool {
	select {
	case ch <- value:
		return true
	default:
		log.Printf("[WARN] Channel %s is full, dropping message (potential deadlock prevented)", name)
		return false
	}
}

// safeSendWithDebug attempts to send to a channel without blocking.
// Returns true if send succeeded, false if channel was full.
// Logs debug message on success and warning on failure (if debug enabled).
func safeSendWithDebug[T any](ch chan T, value T, name string) bool {
	select {
	case ch <- value:
		debugf("sent to channel %s", name)
		return true
	default:
		log.Printf("[WARN] Channel %s is full, dropping message (potential deadlock prevented)", name)
		return false
	}
}
