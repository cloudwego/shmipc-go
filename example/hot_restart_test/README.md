### client path
- build.sh compiling script
- bootstrap.sh start script
  
  env PATH_PREFIX can set the buffer path, different paths are equivalent to multiple clients

### server path
- build.sh compiling script
- bootstrap.sh first start server script
- bootstrap_hot_restart.sh hot restart server script
  

  env IS_HOT_RESTART value is true hot restart
  
  env HOT_RESTART_EPOCH epoch id of the server, each hot restart needs to be different
  
`The hot restart test requires bootstrap.sh to start the server first，bootstrap_hot_restart.sh hot restart the new server by change the value of HOT_RESTART_EPOCH`
