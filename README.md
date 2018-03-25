# Source code for the streaming telemetry presentation at INOG::F

The goal of this little project is to show how you can take advantage of the "streaming"
part of streaming telemetry when you build your network automation systems.
We'll build a simple program that makes sure that the interfaces which are in "admin up"
state are configured with an IP address from the IP database.

The program is written in `Go`, but I have tried to keep it simple and accessible to
people who might not necessarily know the language. Therefore, I mostly stuck to basic
data structures and types (maps, arrays, strings and integers) and either standalone
functions or "methods" (ie, functions called on an instance of a `struct`, similar to
classes and methods in other languages). The only exception to this are `channels`,
which are used by the `gNMI` client library. You can think of them as shell pipes,
but they send data between threads (called `goroutines` in `Go`), rather than processes.
People familiar with the language might find the code not idiomatic/efficient at times.

## The traditional approach and its problems

In a traditional approach we'd get a list of interfaces then check which ones are in
"admin up" and then we'll get their configuration, check it against the database and
adjust it, if necessary. Then we'll wait for a while and repeat the whole process.
One of the problems with this approach is that interface status can change while we're
configuring it, so we have to be careful to check preconditions at each step. Also,
it will be pretty wasteful to get all the interfaces and the configurations periodically
even when things don't change. And at the same time any interfaces coming up will have
to wait up to our sleep interval before their configuration gets validated and/or changed.

## The reactive programming approach and state machines

In this exercise we'll take reactive approach where we'll subscribe to relevant `OpenConfig`
paths and we'll react to every message we receive and take the appropriate action.
As `OpenConfig` streams state, not events (eg, it will send the current interface state,
not the fact that it transitioned), even if we're slow to react to these changes, we can
react to only the latest change and we'll be in the correct state regardless if we dropped
any intermediate changes.

One of the peculiarities of this model is that we're going to receive the state changes for
each fragment of information we're interested in (in our case, admin status, address and
mask), independently and without any ordering guarantees, or any delay guarantees between
the updates (although we will assume a bounded amount of time to receive all the notifications
for one interface). Furthermore, some changes that are done "atomically" on the CLI, will
have independent paths and only those paths that have actually changed will be streamed
(eg, changing address from "1.1.1.1/24" to "1.1.1.2/24" will generate a message only for
IP address, and not for prefix length).

In order to keep track of the fragments we've collected and the steps in our workflow,
we'll use a state machine per interface which will hold the collected (or generated)
address and prefix length and also the current state. Every message we receive from `gNMI`
will fill in some of the data and/or transition the state machine to another state,
depending on the conditions.

Another advantage of thinking in terms of state machines and state changes, is that
it prompts you to think in advance about how you'll handle each change in the state of
the system at every step of your workflow (this is similar to how `Go` or other languages
with explicit error handling prompt you to think how you'll deal with failure at
each step where a failure can occur). This will increase the chances that you'll spot
and address edge cases during your program design, rather than in production.

## Program flow

The general flow of our program will be:

* subscribe to `OpenConfig` paths for admin status, IP address, prefix length.
* parse each message from `gNMI` into an event type and a value.
* dispatch each event to the state machine corresponding to the interface that generated
the event.
* the event handlers will do the following:
	* admin status event handler will transition the state machine to:
		* `adminDown` immediately if the value is "DOWN".
		* `adminUp`, immediately if the value is "UP".
		* `configured`, if value is "UP" and all the configuration has been received.
	* prefix and prefix length handlers will store the values in the state machine and possibly
	  transition the state machine to `configured` if all the configuration fragments have been received
	  and the current state is `adminUp`.
	* the timer event handler will transition the state machine to `configured`.
* the transition methods will do the following:
	* `adminDown` will cancel the timer (if set) and release the IP.
	* `adminUp` will set a timer for 20 seconds, if the configuration hasn't been fully received.
	  We need the timer because we don't know in advance if any address is configured at all, so we'll just wait
	  for a while to see if we'll receive any prefix/prefix length events.
	* `configured` will try to reconcile the current configuration (if any received, or previously generated)
	  with the IP database. If there's no current configuration or reconciliation failed,
	  it will assign a new IP and configure the interface.

See the [state machine diagram](interface.png?raw=true)

## Exercises for the reader
* Can you spot and correct a missing transition in response to an event? (Hint: you'll need another state to avoid config churn)
* How would you handle the case where a previously configured interface will have its configuration removed by CLI/another program?
* How would you re-design the program so that the timer event handler will not access the state machine from another thread?
* How would you get rid of the timer?

