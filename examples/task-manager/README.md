# Task Manager Example

A SQLite-backed task management API that demonstrates all flux primitives: tags, atoms, resources, flows, controllers, select, and extensions.

## Usage

### Seed and demo CRUD

```bash
go run . run
```

Runs a full lifecycle demo: creates a database, seeds sample tasks, performs CRUD operations, and prints results.

### HTTP server

```bash
go run . serve :8080
```

Starts an HTTP server on the given address.

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | /tasks | Create a new task |
| GET | /tasks | List all tasks |
| GET | /tasks/{id} | Get a task by ID |
| PATCH | /tasks/{id} | Update a task |
| DELETE | /tasks/{id} | Delete a task |
| GET | /stats | Get task statistics |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| FLUX_DB_PATH | tasks.db | Path to the SQLite database file |
| LOG_LEVEL | debug | Log verbosity (debug, info, warn, error) |

## Architecture

```mermaid
graph TD
    subgraph Tags
        DBPath[db-path tag]
        LogLevel[log-level tag]
    end

    subgraph Atoms
        DB[db]
        Logger[logger]
        Stats[task-stats]
    end

    subgraph Resources
        DBTX[db-tx]
        ReqID[request-id]
        ReqLogger[req-logger]
    end

    subgraph Flows
        Create[create-task]
        List[list-tasks]
        Get[get-task]
        Update[update-task]
        Delete[delete-task]
    end

    subgraph Extensions
        Timing[request-timing]
    end

    DBPath --> DB
    LogLevel --> Logger
    DB --> Stats
    DB --> DBTX
    ReqID --> ReqLogger
    Logger --> ReqLogger

    DBTX --> Create
    DBTX --> List
    DBTX --> Get
    DBTX --> Update
    DBTX --> Delete

    ReqLogger --> Create
    ReqLogger --> List
    ReqLogger --> Get
    ReqLogger --> Update
    ReqLogger --> Delete

    Get -.-> Update
```
