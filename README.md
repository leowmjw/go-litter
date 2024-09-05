# go-litter
Golem Cloud Hackathon 2024 - GoLitter - Twitter Clone

Inspired by the article [How we reduced the cost of building Twitter at Twitter-scale by 100x](https://blog.redplanetlabs.com/2023/08/15/how-we-reduced-the-cost-of-building-twitter-at-twitter-scale-by-100x/#Representing_data)
; using the Rama Architecture by the Redplanet Lab folks!

Its DataFlow model can be done equivalently using the Golem Cloud Architecture; similar to how [Golem Timeline](https://github.com/afsalthaj/golem-timeline)

## Instructions
- Command to run ; know worldode ..

```./gcloud-cli stubgen generate --source-wit-root go-litter/components/status/wit --dest-crate-root go-litter/components/status_stub```



## High Level Design

The initial naive port will start with GoLitter App, Account, Status, Follow-And-Blacklist Depots.

See the complete Architecture to port:
![Architecture](https://i0.wp.com/blog.redplanetlabs.com/wp-content/uploads/2023/07/timelines-diagram.png?w=1312&ssl=1)


### GoLitter App

- This is the main app; a singleton and handling high level actions; transform to OpenAPI
- Equivalent of the Account Depot + AccountEdit Depot combined for simple naive case
- It auto-creates accounts when "login"; uses a naive incremental counter
- It maintains a PSet of username to AccountID; which is used in all
- Future:
  - Deleting user; propagating removal of followers + content tracked by followers
  - Changing username allowed; while still checking global clash
  - SessionActive mapping ..

### Account Component

- Each Account Worker Represents one Worker; might be too extreme but this will horizotal scale nicely as it
  will be primarily processing things like follow, unfollow, conversations, blacklist ..
- Account maintains a StatusEvents chnages ..
- Maintains a PSet of StatusDetails, Follows, Followers, StatusPostsDrafts
- Render Follow List Contents
- Accept RPC for Follow or Blacklist Action
- Future:
  - Partition Accounts by size of postings, followers?

### Status Component

- Equivalent of the Status Depot + StatusWithID Depot combined for simple naive case
- Status will be removed
- Future: 
  - Status will represent all Status added per day; partitioned by DatePosted.  
    This will be immutable; will be used as source for backend analytics.  Delete action will map to DateRemoved.
  - StatusWithId will represent all active Status Messages currently; partitioned by StatusID in batch of 10000? 
    For testing we will use the smaller size of 10 

### Follow-And-Blacklist Component

- Equivalent of the FollowAndBlacklist Depot
- Add immutable ordered of actions partitioned by DateActionTaken; Type Follow or Blacklist
- Based on the AccountID taking the action; call RPC of Account worker with the proper input
